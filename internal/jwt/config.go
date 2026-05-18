// Package jwt mints signed JWTs for outbound Alpheya service calls.
//
// It is a Go port of ai-agent/assay's TokenSigningProvider
// (src/adapters/auth/token-signing.ts). Same claim shape, same RS256/HS256
// algorithms, same `service:profile` per-request identity with silent
// fallback to default claims when a profile is absent.
//
// The package is consumed by cmd/kelos-jwt (explicit CLI) and
// cmd/kelos-curl (transparent curl wrapper that injects Authorization on
// hosts that match ALPHEYA_TOKEN_SIGNING_HOSTS). Both binaries read their
// configuration from environment variables only — there is no
// TaskSpawner CRD schema dependency, so config flows through the
// existing TaskSpawner.spec.taskTemplate.podOverrides.env field.
package jwt

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Algorithm is the JWS signing algorithm. Only RS256 and HS256 are
// supported to keep parity with assay's TokenSigningConfig.
type Algorithm string

const (
	AlgorithmRS256 Algorithm = "RS256"
	AlgorithmHS256 Algorithm = "HS256"
)

// IdentityProfile is a set of claims for a single principal. Matches
// assay's IdentityProfileSchema (config/schema.ts:68) with two
// Alpheya-specific extensions:
//   - Ext carries the nested `ext` object some downstream services
//     read (e.g., ext.sub, ext.preferred_username) — see the oauth2-proxy
//     token shape in non-prod.
//   - Aud is intentionally NOT here; the audience is consistent across
//     all profiles and lives on TokenSigningConfig.
type IdentityProfile struct {
	Sub   string         `json:"sub"`
	Roles []string       `json:"roles"`
	Email string         `json:"email,omitempty"`
	Name  string         `json:"name,omitempty"`
	Ext   map[string]any `json:"ext,omitempty"`
}

// TokenSigningConfig is the full signing configuration. KeyMaterial is
// the raw key bytes (PEM for RS256, secret bytes for HS256).
type TokenSigningConfig struct {
	Algorithm        Algorithm
	KeyID            string
	Issuer           string
	Audience         string
	ExpiresInSeconds int
	DefaultClaims    IdentityProfile
	Profiles         map[string]IdentityProfile
	KeyMaterial      []byte
}

const (
	EnvAlgorithm     = "ALPHEYA_TOKEN_SIGNING_ALGORITHM"
	EnvKeyID         = "ALPHEYA_TOKEN_SIGNING_KEY_ID"
	EnvIssuer        = "ALPHEYA_TOKEN_SIGNING_ISSUER"
	EnvAudience      = "ALPHEYA_TOKEN_SIGNING_AUDIENCE"
	EnvExpiresIn     = "ALPHEYA_TOKEN_SIGNING_EXPIRES_IN"
	EnvDefaultClaims = "ALPHEYA_TOKEN_SIGNING_DEFAULT_CLAIMS"
	EnvProfiles      = "ALPHEYA_TOKEN_SIGNING_PROFILES"
	EnvKey           = "ALPHEYA_TOKEN_SIGNING_KEY"
	EnvKeyFile       = "ALPHEYA_TOKEN_SIGNING_KEY_FILE"
)

// LoadConfigFromEnv reads the signing configuration from environment
// variables. Required values produce descriptive errors rather than
// silent defaults so a misconfigured pod fails loudly at startup.
func LoadConfigFromEnv() (*TokenSigningConfig, error) {
	cfg := &TokenSigningConfig{
		Algorithm:        AlgorithmRS256,
		ExpiresInSeconds: 3600,
	}

	if v := os.Getenv(EnvAlgorithm); v != "" {
		switch Algorithm(v) {
		case AlgorithmRS256, AlgorithmHS256:
			cfg.Algorithm = Algorithm(v)
		default:
			return nil, fmt.Errorf("%s: unsupported algorithm %q (want RS256 or HS256)", EnvAlgorithm, v)
		}
	}

	cfg.KeyID = os.Getenv(EnvKeyID)

	cfg.Issuer = os.Getenv(EnvIssuer)
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("%s is required", EnvIssuer)
	}

	// Audience is optional in the type system but required in practice
	// for any Alpheya service that validates aud. Left optional here so
	// callers writing tests or non-Alpheya integrations can omit it.
	cfg.Audience = os.Getenv(EnvAudience)

	if v := os.Getenv(EnvExpiresIn); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvExpiresIn, err)
		}
		if n < 60 || n > 86400 {
			return nil, fmt.Errorf("%s: %d out of range [60, 86400]", EnvExpiresIn, n)
		}
		cfg.ExpiresInSeconds = n
	}

	defaults := os.Getenv(EnvDefaultClaims)
	if defaults == "" {
		return nil, fmt.Errorf("%s is required (JSON identity profile)", EnvDefaultClaims)
	}
	if err := json.Unmarshal([]byte(defaults), &cfg.DefaultClaims); err != nil {
		return nil, fmt.Errorf("%s: %w", EnvDefaultClaims, err)
	}
	if cfg.DefaultClaims.Sub == "" {
		return nil, fmt.Errorf("%s: missing required 'sub'", EnvDefaultClaims)
	}

	if v := os.Getenv(EnvProfiles); v != "" {
		if err := json.Unmarshal([]byte(v), &cfg.Profiles); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvProfiles, err)
		}
		for name, p := range cfg.Profiles {
			if p.Sub == "" {
				return nil, fmt.Errorf("%s: profile %q missing required 'sub'", EnvProfiles, name)
			}
		}
	}

	key, err := loadKeyMaterial()
	if err != nil {
		return nil, err
	}
	cfg.KeyMaterial = key

	return cfg, nil
}

func loadKeyMaterial() ([]byte, error) {
	if v := os.Getenv(EnvKey); v != "" {
		// Sealed-secret env vars round-trip PEM newlines as literal `\n`;
		// match assay's token-signing.ts:35 fix-up.
		return []byte(strings.ReplaceAll(v, `\n`, "\n")), nil
	}
	if path := os.Getenv(EnvKeyFile); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvKeyFile, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("signing key not provided (set %s or %s)", EnvKey, EnvKeyFile)
}
