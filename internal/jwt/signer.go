package jwt

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Signer mints compact JWTs. Construct with NewSigner; call Sign per
// outbound request. No in-process caching — assay's 50% TTL cache makes
// sense for a long-lived service, not for a process invoked per shell
// command. If signing cost shows up in profiles, revisit.
type Signer struct {
	cfg     *TokenSigningConfig
	rsaKey  *rsa.PrivateKey
	hmacKey []byte
	now     func() time.Time // injected for tests
}

// NewSigner validates the config and parses the signing key once.
func NewSigner(cfg *TokenSigningConfig) (*Signer, error) {
	if cfg == nil {
		return nil, errors.New("jwt: nil config")
	}
	s := &Signer{cfg: cfg, now: time.Now}
	switch cfg.Algorithm {
	case AlgorithmRS256:
		key, err := parseRSAPrivateKey(cfg.KeyMaterial)
		if err != nil {
			return nil, fmt.Errorf("jwt: parse RSA key: %w", err)
		}
		s.rsaKey = key
	case AlgorithmHS256:
		if len(cfg.KeyMaterial) == 0 {
			return nil, errors.New("jwt: empty HS256 key")
		}
		s.hmacKey = cfg.KeyMaterial
	default:
		return nil, fmt.Errorf("jwt: unsupported algorithm %q", cfg.Algorithm)
	}
	return s, nil
}

// Sign produces a compact JWT for the named service. If `service`
// contains ":", the suffix is treated as a profile name; lookup falls
// back to default claims when the profile is absent. Mirrors assay's
// token-signing.ts resolveClaims (lines 138-146).
func (s *Signer) Sign(service string) (string, error) {
	claims := s.resolveClaims(service)
	if claims.Sub == "" {
		return "", errors.New("jwt: resolved claims missing 'sub'")
	}

	header := map[string]any{
		"alg": string(s.cfg.Algorithm),
		"typ": "JWT",
	}
	if s.cfg.KeyID != "" {
		header["kid"] = s.cfg.KeyID
	}

	now := s.now().Unix()
	roles := claims.Roles
	if roles == nil {
		// Emit [] over null so verifiers expecting a roles array don't trip.
		roles = []string{}
	}
	payload := map[string]any{
		"sub":   claims.Sub,
		"roles": roles,
		"iat":   now,
		"exp":   now + int64(s.cfg.ExpiresInSeconds),
		"iss":   s.cfg.Issuer,
	}
	if s.cfg.Audience != "" {
		payload["aud"] = s.cfg.Audience
	}
	if claims.Email != "" {
		payload["email"] = claims.Email
	}
	if claims.Name != "" {
		payload["name"] = claims.Name
	}
	if len(claims.Ext) > 0 {
		payload["ext"] = claims.Ext
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal header: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal payload: %w", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := headerB64 + "." + payloadB64

	sig, err := s.sign([]byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *Signer) resolveClaims(service string) IdentityProfile {
	idx := strings.Index(service, ":")
	if idx == -1 {
		return s.cfg.DefaultClaims
	}
	name := service[idx+1:]
	if p, ok := s.cfg.Profiles[name]; ok {
		return p
	}
	return s.cfg.DefaultClaims
}

func (s *Signer) sign(input []byte) ([]byte, error) {
	switch s.cfg.Algorithm {
	case AlgorithmRS256:
		hashed := sha256.Sum256(input)
		return rsa.SignPKCS1v15(rand.Reader, s.rsaKey, crypto.SHA256, hashed[:])
	case AlgorithmHS256:
		mac := hmac.New(sha256.New, s.hmacKey)
		mac.Write(input)
		return mac.Sum(nil), nil
	}
	return nil, fmt.Errorf("jwt: unreachable: algorithm %q", s.cfg.Algorithm)
}

// parseRSAPrivateKey accepts PKCS#8 ("BEGIN PRIVATE KEY") or PKCS#1
// ("BEGIN RSA PRIVATE KEY"). assay's token-signing.ts:50-63 does the
// same auto-conversion; this version skips the conversion step because
// Go's stdlib parses PKCS#1 natively.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS#8 key is not RSA")
		}
		return rsaKey, nil
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM type %q", block.Type)
	}
}
