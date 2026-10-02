package consoleserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AuthModeStaticToken = "staticToken"
	AuthModeOIDC        = "oidc"
)

// AccessReviewer delegates authorization to the Kubernetes API server.
type AccessReviewer interface {
	Create(context.Context, *authorizationv1.SubjectAccessReview, metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error)
}

// OIDCConfig configures identities supplied by the managed loopback proxy.
// ExternalURL is the HTTPS origin of the Console, without a path.
type OIDCConfig struct {
	ExternalURL    string
	UsernamePrefix string
	GroupsPrefix   string
	Reviewer       AccessReviewer
	Logger         *slog.Logger
}

type principal struct {
	username string
	groups   []string
}

type principalKey struct{}

func validateAuth(config Config) error {
	switch config.AuthMode {
	case "", AuthModeStaticToken:
		if config.OIDC != nil {
			return errors.New("OIDC configuration requires oidc authentication mode")
		}
		if strings.TrimSpace(config.Token) == "" {
			return errors.New("static authentication token must not be empty")
		}
	case AuthModeOIDC:
		if config.Token != "" || config.SecureCookie {
			return errors.New("static token settings cannot be used with oidc authentication")
		}
		if config.OIDC == nil || config.OIDC.Reviewer == nil || config.OIDC.Logger == nil {
			return errors.New("OIDC configuration, access reviewer and audit logger are required")
		}
		for _, prefix := range []string{config.OIDC.UsernamePrefix, config.OIDC.GroupsPrefix} {
			if !validIdentityValue(prefix, 128) || strings.HasPrefix(prefix, "system:") || strings.HasPrefix("system:", prefix) {
				return errors.New("identity prefixes must be non-empty, at most 128 bytes and must not overlap the reserved system: prefix")
			}
		}
		origin, err := url.Parse(config.OIDC.ExternalURL)
		if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.ForceQuery {
			return errors.New("OIDC external URL must be an HTTPS origin without credentials, path, query or fragment")
		}
	default:
		return fmt.Errorf("unsupported authentication mode %q", config.AuthMode)
	}
	return nil
}

func validIdentityValue(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.Contains(value, ",") &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func (s *Server) proxyPrincipal(request *http.Request) (principal, bool) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return principal{}, false
	}
	users := request.Header.Values("X-Kelos-User")
	if len(users) != 1 || !validIdentityValue(users[0], 1024) {
		return principal{}, false
	}
	identity := principal{username: s.oidc.UsernamePrefix + users[0], groups: []string{}}
	groups := request.Header.Values("X-Kelos-Groups")
	if len(groups) > 1 {
		return principal{}, false
	}
	if len(groups) == 1 {
		if len(groups[0]) > 8192 {
			return principal{}, false
		}
		values := strings.Split(groups[0], ",")
		if len(values) > 128 {
			return principal{}, false
		}
		for _, group := range values {
			if !validIdentityValue(group, 256) {
				return principal{}, false
			}
			identity.groups = append(identity.groups, s.oidc.GroupsPrefix+group)
		}
	}
	return identity, true
}

func (s *Server) requireOIDC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, ok := s.proxyPrincipal(request)
		if !ok {
			writeError(writer, http.StatusUnauthorized, "authentication required")
			return
		}
		// The proxy owns cookies; bind browser writes and upgrades to the configured origin.
		if request.Method != http.MethodGet && request.Method != http.MethodHead || strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
			origins := request.Header.Values("Origin")
			if len(origins) != 1 || origins[0] != s.oidc.ExternalURL {
				writeError(writer, http.StatusForbidden, "request origin is not allowed")
				return
			}
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalKey{}, identity)))
	})
}

func access(verb, resource, namespace, name string) authorizationv1.ResourceAttributes {
	resource, subresource, _ := strings.Cut(resource, "/")
	return authorizationv1.ResourceAttributes{Group: "kelos.dev", Resource: resource, Subresource: subresource, Verb: verb, Namespace: namespace, Name: name}
}

func (s *Server) allowed(request *http.Request, attributes authorizationv1.ResourceAttributes) (bool, error) {
	if s.oidc == nil {
		return true, nil
	}
	identity, ok := request.Context().Value(principalKey{}).(principal)
	if !ok {
		return false, errors.New("authenticated principal is missing")
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	review, err := s.oidc.Reviewer.Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{User: identity.username, Groups: identity.groups, ResourceAttributes: &attributes},
	}, metav1.CreateOptions{})
	allowed := false
	decision := "denied"
	if err == nil && (review == nil || review.Status.EvaluationError != "") {
		err = errors.New("authorization evaluation failed")
	}
	if err != nil {
		decision = "error"
	} else if review.Status.Allowed && !review.Status.Denied {
		allowed = true
		decision = "allowed"
	}
	s.oidc.Logger.InfoContext(request.Context(), "Console access reviewed",
		"username", identity.username, "groups", identity.groups, "action", attributes.Verb,
		"decision", decision, "namespace", attributes.Namespace, "apiGroup", attributes.Group, "resource", attributes.Resource,
		"subresource", attributes.Subresource, "name", attributes.Name)
	return allowed, err
}

func (s *Server) requireAccess(writer http.ResponseWriter, request *http.Request, checks ...authorizationv1.ResourceAttributes) bool {
	for _, attributes := range checks {
		allowed, err := s.allowed(request, attributes)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
			return false
		}
		if !allowed {
			writeError(writer, http.StatusForbidden, "access denied")
			return false
		}
	}
	return true
}
