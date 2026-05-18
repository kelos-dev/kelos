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
	"os"
	"strings"
	"testing"
	"time"
)

func newTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return key, pemBytes
}

func decodeSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode segment %q: %v", seg, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal segment: %v", err)
	}
	return out
}

func splitJWT(t *testing.T, token string) (header, payload, sig string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 segments, got %d in %q", len(parts), token)
	}
	return parts[0], parts[1], parts[2]
}

func TestSign_RS256_VerifiesAgainstPublicKey(t *testing.T) {
	priv, pemBytes := newTestKey(t)
	signer, err := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "test-issuer",
		ExpiresInSeconds: 3600,
		DefaultClaims:    IdentityProfile{Sub: "cody", Roles: []string{"debug"}, Email: "cody@alpheya.com"},
		KeyMaterial:      pemBytes,
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	jwt, err := signer.Sign("some-service")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	h, p, s := splitJWT(t, jwt)
	signingInput := h + "." + p
	hashed := sha256.Sum256([]byte(signingInput))
	sigBytes, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, hashed[:], sigBytes); err != nil {
		t.Fatalf("signature did not verify: %v", err)
	}

	payload := decodeSegment(t, p)
	if got := payload["sub"]; got != "cody" {
		t.Errorf("sub = %v, want cody", got)
	}
	if got := payload["iss"]; got != "test-issuer" {
		t.Errorf("iss = %v, want test-issuer", got)
	}
	if got := payload["email"]; got != "cody@alpheya.com" {
		t.Errorf("email = %v, want cody@alpheya.com", got)
	}
	roles, ok := payload["roles"].([]any)
	if !ok || len(roles) != 1 || roles[0] != "debug" {
		t.Errorf("roles = %v, want [debug]", payload["roles"])
	}
	if _, ok := payload["iat"].(float64); !ok {
		t.Errorf("iat not numeric: %v", payload["iat"])
	}
	if _, ok := payload["exp"].(float64); !ok {
		t.Errorf("exp not numeric: %v", payload["exp"])
	}

	header := decodeSegment(t, h)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("header = %v, want alg=RS256 typ=JWT", header)
	}
}

func TestSign_HS256_HMACMatchesFreshComputation(t *testing.T) {
	key := []byte("hmac-secret-bytes")
	signer, err := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmHS256,
		Issuer:           "test",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		KeyMaterial:      key,
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	jwt, err := signer.Sign("foo")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	h, p, s := splitJWT(t, jwt)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h + "." + p))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if s != want {
		t.Errorf("HS256 sig mismatch:\n got: %s\nwant: %s", s, want)
	}
}

func TestSign_ProfileOverridesDefaults(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, err := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody", Roles: []string{"debug"}},
		Profiles: map[string]IdentityProfile{
			"admin": {Sub: "cody-admin", Roles: []string{"admin", "debug"}},
		},
		KeyMaterial: pemBytes,
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	jwt, err := signer.Sign("order-service:admin")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)
	if payload["sub"] != "cody-admin" {
		t.Errorf("profile sub = %v, want cody-admin", payload["sub"])
	}
	roles, _ := payload["roles"].([]any)
	if len(roles) != 2 || roles[0] != "admin" || roles[1] != "debug" {
		t.Errorf("profile roles = %v, want [admin debug]", payload["roles"])
	}
}

func TestSign_UnknownProfileFallsBackToDefaults(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody", Roles: []string{"debug"}},
		Profiles: map[string]IdentityProfile{
			"admin": {Sub: "cody-admin", Roles: []string{"admin"}},
		},
		KeyMaterial: pemBytes,
	})
	jwt, err := signer.Sign("order-service:nonexistent")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, p, _ := splitJWT(t, jwt)
	if decodeSegment(t, p)["sub"] != "cody" {
		t.Errorf("expected fallback to default sub=cody, got %v", decodeSegment(t, p)["sub"])
	}
}

func TestSign_KidIncludedWhenSet(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		KeyID:            "my-key-id",
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		KeyMaterial:      pemBytes,
	})
	jwt, err := signer.Sign("foo")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	h, _, _ := splitJWT(t, jwt)
	header := decodeSegment(t, h)
	if header["kid"] != "my-key-id" {
		t.Errorf("kid = %v, want my-key-id", header["kid"])
	}
}

func TestSign_AudIncludedWhenSet(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "https://auth.qwlth.dev",
		Audience:         "alpheya",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		KeyMaterial:      pemBytes,
	})
	jwt, err := signer.Sign("foo")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)
	if payload["aud"] != "alpheya" {
		t.Errorf("aud = %v, want alpheya", payload["aud"])
	}
}

func TestSign_AudOmittedWhenUnset(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		KeyMaterial:      pemBytes,
	})
	jwt, _ := signer.Sign("foo")
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)
	if _, present := payload["aud"]; present {
		t.Errorf("aud should be absent when Audience is empty; got %v", payload["aud"])
	}
}

func TestSign_ExtNestedClaimsRoundTrip(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		Audience:         "alpheya",
		ExpiresInSeconds: 60,
		DefaultClaims: IdentityProfile{
			Sub:   "6ab6d10e-5f43-4e74-9dca-e99e7c7c73dd",
			Roles: []string{"all_access:int", "iam_admin"},
			Email: "bobby@alpheya.com",
			Name:  "Bobby Donchev",
			Ext: map[string]any{
				"sub":                "3abc9f82-ca4b-49ad-b3d2-3fe9723ed2e5",
				"preferred_username": "bobby@alpheya.com",
			},
		},
		KeyMaterial: pemBytes,
	})
	jwt, err := signer.Sign("hermes")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)

	ext, ok := payload["ext"].(map[string]any)
	if !ok {
		t.Fatalf("ext not present or wrong type: %#v", payload["ext"])
	}
	if ext["sub"] != "3abc9f82-ca4b-49ad-b3d2-3fe9723ed2e5" {
		t.Errorf("ext.sub = %v", ext["sub"])
	}
	if ext["preferred_username"] != "bobby@alpheya.com" {
		t.Errorf("ext.preferred_username = %v", ext["preferred_username"])
	}
	// Top-level claims still present alongside ext.
	if payload["sub"] != "6ab6d10e-5f43-4e74-9dca-e99e7c7c73dd" {
		t.Errorf("top-level sub clobbered: %v", payload["sub"])
	}
}

func TestSign_ExtOmittedWhenEmpty(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		KeyMaterial:      pemBytes,
	})
	jwt, _ := signer.Sign("foo")
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)
	if _, present := payload["ext"]; present {
		t.Errorf("ext should be absent when empty; got %v", payload["ext"])
	}
}

func TestSign_RolesNilEmitsEmptyArray(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody", Roles: nil},
		KeyMaterial:      pemBytes,
	})
	jwt, _ := signer.Sign("foo")
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)
	roles, ok := payload["roles"].([]any)
	if !ok {
		t.Fatalf("roles is not an array: %#v", payload["roles"])
	}
	if len(roles) != 0 {
		t.Errorf("expected empty roles, got %v", roles)
	}
}

func TestSign_ExpAndIatRespectInjectedClock(t *testing.T) {
	_, pemBytes := newTestKey(t)
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 600,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		KeyMaterial:      pemBytes,
	})
	fixed := time.Unix(1_700_000_000, 0)
	signer.now = func() time.Time { return fixed }

	jwt, _ := signer.Sign("foo")
	_, p, _ := splitJWT(t, jwt)
	payload := decodeSegment(t, p)
	if payload["iat"].(float64) != 1_700_000_000 {
		t.Errorf("iat = %v, want 1700000000", payload["iat"])
	}
	if payload["exp"].(float64) != 1_700_000_600 {
		t.Errorf("exp = %v, want 1700000600", payload["exp"])
	}
}

func TestNewSigner_RejectsEmptyHS256Key(t *testing.T) {
	_, err := NewSigner(&TokenSigningConfig{
		Algorithm:     AlgorithmHS256,
		Issuer:        "t",
		DefaultClaims: IdentityProfile{Sub: "cody"},
		KeyMaterial:   nil,
	})
	if err == nil {
		t.Fatal("expected error for empty HS256 key")
	}
}

func TestNewSigner_RejectsBadPEM(t *testing.T) {
	_, err := NewSigner(&TokenSigningConfig{
		Algorithm:     AlgorithmRS256,
		Issuer:        "t",
		DefaultClaims: IdentityProfile{Sub: "cody"},
		KeyMaterial:   []byte("not a pem"),
	})
	if err == nil {
		t.Fatal("expected error for bad PEM")
	}
}

func TestSign_RejectsResolvedClaimsWithoutSub(t *testing.T) {
	_, pemBytes := newTestKey(t)
	// Build a Signer with a Default sub then mutate to simulate a profile
	// that resolved to empty sub at sign time.
	signer, _ := NewSigner(&TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		Issuer:           "t",
		ExpiresInSeconds: 60,
		DefaultClaims:    IdentityProfile{Sub: "cody"},
		Profiles: map[string]IdentityProfile{
			"broken": {Sub: ""},
		},
		KeyMaterial: pemBytes,
	})
	if _, err := signer.Sign("svc:broken"); err == nil {
		t.Fatal("expected error when resolved claims have empty sub")
	}
}

func TestLoadConfigFromEnv_RoundTrip(t *testing.T) {
	_, pemBytes := newTestKey(t)
	t.Setenv(EnvIssuer, "test-issuer")
	t.Setenv(EnvAudience, "alpheya")
	t.Setenv(EnvKey, string(pemBytes))
	t.Setenv(EnvDefaultClaims, `{"sub":"cody","roles":["debug"],"ext":{"preferred_username":"cody@alpheya.com"}}`)
	t.Setenv(EnvProfiles, `{"admin":{"sub":"cody-admin","roles":["admin"]}}`)
	t.Setenv(EnvKeyID, "kid1")
	t.Setenv(EnvExpiresIn, "120")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.Issuer != "test-issuer" || cfg.Audience != "alpheya" || cfg.KeyID != "kid1" || cfg.ExpiresInSeconds != 120 {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
	if cfg.DefaultClaims.Ext["preferred_username"] != "cody@alpheya.com" {
		t.Errorf("ext.preferred_username not loaded: %+v", cfg.DefaultClaims.Ext)
	}
	if cfg.DefaultClaims.Sub != "cody" || len(cfg.DefaultClaims.Roles) != 1 {
		t.Errorf("default claims: %+v", cfg.DefaultClaims)
	}
	if cfg.Profiles["admin"].Sub != "cody-admin" {
		t.Errorf("profile: %+v", cfg.Profiles)
	}
	if _, err := NewSigner(cfg); err != nil {
		t.Errorf("NewSigner from env cfg: %v", err)
	}
}

func TestLoadConfigFromEnv_MissingIssuer(t *testing.T) {
	t.Setenv(EnvIssuer, "")
	t.Setenv(EnvKey, "x")
	t.Setenv(EnvDefaultClaims, `{"sub":"x"}`)
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for missing issuer")
	}
}

func TestLoadConfigFromEnv_MissingDefaultClaims(t *testing.T) {
	t.Setenv(EnvIssuer, "t")
	t.Setenv(EnvKey, "x")
	t.Setenv(EnvDefaultClaims, "")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for missing default claims")
	}
}

func TestLoadConfigFromEnv_MissingSubInDefault(t *testing.T) {
	t.Setenv(EnvIssuer, "t")
	t.Setenv(EnvKey, "x")
	t.Setenv(EnvDefaultClaims, `{"roles":[]}`)
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for default claims missing sub")
	}
}

func TestLoadConfigFromEnv_UnescapesLiteralNewlinesInKey(t *testing.T) {
	_, pemBytes := newTestKey(t)
	escaped := strings.ReplaceAll(string(pemBytes), "\n", `\n`)
	t.Setenv(EnvIssuer, "t")
	t.Setenv(EnvKey, escaped)
	t.Setenv(EnvDefaultClaims, `{"sub":"x"}`)

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if _, err := NewSigner(cfg); err != nil {
		t.Errorf("expected escaped \\n to be unescaped; NewSigner: %v", err)
	}
}

func TestLoadConfigFromEnv_KeyFileFallback(t *testing.T) {
	_, pemBytes := newTestKey(t)
	dir := t.TempDir()
	keyPath := dir + "/key.pem"
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	t.Setenv(EnvIssuer, "t")
	t.Setenv(EnvKey, "")
	t.Setenv(EnvKeyFile, keyPath)
	t.Setenv(EnvDefaultClaims, `{"sub":"x"}`)

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if _, err := NewSigner(cfg); err != nil {
		t.Errorf("NewSigner with KEY_FILE: %v", err)
	}
}

func TestLoadConfigFromEnv_RejectsBadAlgorithm(t *testing.T) {
	t.Setenv(EnvAlgorithm, "ES256")
	t.Setenv(EnvIssuer, "t")
	t.Setenv(EnvKey, "x")
	t.Setenv(EnvDefaultClaims, `{"sub":"x"}`)
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
}

func TestLoadConfigFromEnv_RejectsExpiresInOutOfRange(t *testing.T) {
	t.Setenv(EnvIssuer, "t")
	t.Setenv(EnvKey, "x")
	t.Setenv(EnvDefaultClaims, `{"sub":"x"}`)
	t.Setenv(EnvExpiresIn, "10")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for too-small ExpiresIn")
	}
	t.Setenv(EnvExpiresIn, "999999")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for too-large ExpiresIn")
	}
}

