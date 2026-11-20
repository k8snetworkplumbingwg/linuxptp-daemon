// Package auth provides native CEPv2 TLS and bearer-token authentication.
package auth

import (
	"context"
	"crypto/tls"
	"strings"
)

const httpsScheme = "https"

// ServerConfig contains inbound-server authentication and cluster TLS profile settings.
type ServerConfig struct {
	// mTLS configuration - works for both single and multi-node clusters
	EnableMTLS     bool   `json:"enableMTLS"`
	CACertPath     string `json:"caCertPath"`
	ServerCertPath string `json:"serverCertPath"`
	ServerKeyPath  string `json:"serverKeyPath"`
	// ClientCertPath/ClientKeyPath are the certificate this server presents when
	// it acts as an mTLS *client* for outbound pushes (the initial-notification
	// and publisher-endpoint-validation POSTs). A dedicated clientAuth keypair is
	// required because the server's own serving certificate is serverAuth-only
	// and a crypto/tls server rejects it as a client cert. When empty the push
	// client presents no certificate (prior behavior), which an mTLS-enforcing
	// callback rejects with "client certificate required".
	ClientCertPath string `json:"clientCertPath"`
	ClientKeyPath  string `json:"clientKeyPath"`
	UseServiceCA   bool   `json:"useServiceCA"` // Use OpenShift Service CA (recommended for all cluster sizes)

	// OAuth 2.0 / bearer-token configuration. Tokens are validated by the
	// injected TokenValidator using the Kubernetes TokenReview API, so no
	// issuer/JWKS is configured here.
	EnableOAuth         bool     `json:"enableOAuth"`
	RequiredAudiences   []string `json:"requiredAudiences"`   // Required token audiences (validated by TokenReview)
	ServiceAccountName  string   `json:"serviceAccountName"`  // ServiceAccount used by clients for authentication
	ServiceAccountToken string   `json:"serviceAccountToken"` // ServiceAccount token path (client side)
	UseOpenShiftOAuth   bool     `json:"useOpenShiftOAuth"`   // Compatibility hint; the token is read from ServiceAccountToken

	// TLS profile - centrally managed by the cluster's TLSSecurityProfile and
	// propagated by the operator, subject to the TLS 1.2 security floor.
	TLSMinVersion   string   `json:"tlsMinVersion"`   // e.g. "VersionTLS12", "VersionTLS13"
	TLSCipherSuites []string `json:"tlsCipherSuites"` // IANA cipher suite names
}

// TokenInfo identifies a bearer token authenticated by the cluster.
type TokenInfo struct {
	Username  string
	UID       string
	Groups    []string
	Audiences []string
}

// TokenValidator validates an OAuth 2.0 / OIDC bearer token and returns the
// authenticated identity. Implementations are supplied by the embedding
// application; TokenReviewValidator delegates validation to the cluster.
//
// A validator MUST cryptographically verify the token (signature and, when
// audiences are supplied, audience binding). Returning a non-nil error MUST
// cause the request to be rejected with 401.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string, audiences []string) (*TokenInfo, error)
}

func tlsVersionFromString(v string) uint16 {
	switch strings.TrimSpace(v) {
	case "VersionTLS10":
		return tls.VersionTLS10
	case "VersionTLS11":
		return tls.VersionTLS11
	case "VersionTLS12":
		return tls.VersionTLS12
	case "VersionTLS13":
		return tls.VersionTLS13
	default:
		return 0
	}
}

// cipherSuitesFromNames maps IANA cipher suite names to their crypto/tls IDs.
// Unknown names are ignored. TLS 1.3 cipher suites are not configurable in Go
// and are silently dropped, which is expected.
func cipherSuitesFromNames(names []string) []uint16 {
	if len(names) == 0 {
		return nil
	}
	lookup := make(map[string]uint16)
	for _, cs := range tls.CipherSuites() {
		lookup[cs.Name] = cs.ID
	}
	for _, cs := range tls.InsecureCipherSuites() {
		lookup[cs.Name] = cs.ID
	}
	var out []uint16
	for _, n := range names {
		if id, ok := lookup[strings.TrimSpace(n)]; ok {
			out = append(out, id)
		}
	}
	return out
}

// ApplyTLSProfile applies the centrally-managed TLS profile (min version and
// cipher suites, sourced from the cluster's TLSSecurityProfile via the
// operator) onto a tls.Config. TLS 1.2 is the minimum supported protocol, even
// if an older or unknown version is supplied.
func (c *ServerConfig) ApplyTLSProfile(cfg *tls.Config) {
	if c == nil {
		return
	}
	if mv := tlsVersionFromString(c.TLSMinVersion); mv >= tls.VersionTLS12 {
		cfg.MinVersion = mv
	} else if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}
	if cs := cipherSuitesFromNames(c.TLSCipherSuites); len(cs) > 0 {
		cfg.CipherSuites = cs
	}
}
