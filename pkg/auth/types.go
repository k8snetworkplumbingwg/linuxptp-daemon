// Package auth provides native CEPv2 TLS and bearer-token authentication.
package auth

import (
	"context"
	"crypto/tls"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	libcrypto "github.com/openshift/library-go/pkg/crypto"
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

	// OAuth 2.0 / bearer-token configuration. Tokens are validated by the
	// injected TokenValidator using the Kubernetes TokenReview API, so no
	// issuer/JWKS is configured here.
	EnableOAuth         bool     `json:"enableOAuth"`
	RequiredAudiences   []string `json:"requiredAudiences"`   // Required token audiences (validated by TokenReview)
	ServiceAccountToken string   `json:"serviceAccountToken"` // ServiceAccount token path (client side)

	// TLS profile - centrally managed by the cluster's TLSSecurityProfile and
	// propagated by the operator, subject to the TLS 1.2 security floor.
	TLSGroups       []configv1.TLSGroup `json:"tlsGroups,omitempty"`
	TLSMinVersion   string              `json:"tlsMinVersion"`   // e.g. "VersionTLS12", "VersionTLS13"
	TLSCipherSuites []string            `json:"tlsCipherSuites"` // IANA cipher suite names
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

// ApplyTLSProfile uses the same OpenShift profile translator as the operator.
func (c *ServerConfig) ApplyTLSProfile(cfg *tls.Config) error {
	if c == nil {
		return nil
	}
	version, err := libcrypto.TLSVersion(c.TLSMinVersion)
	if err != nil {
		return err
	}
	minimum := max(version, tls.VersionTLS12)
	previousMinimum := cfg.MinVersion
	profile := configv1.TLSProfileSpec{
		MinTLSVersion: configv1.TLSProtocolVersion(libcrypto.TLSVersionToNameOrDie(minimum)),
		Ciphers:       c.TLSCipherSuites,
		Groups:        c.TLSGroups,
	}
	apply, unsupported := openshifttls.NewTLSConfigFromProfile(profile)
	if len(unsupported) > 0 {
		return fmt.Errorf("unsupported TLS profile entries: %v", unsupported)
	}
	apply(cfg)
	cfg.MinVersion = max(previousMinimum, cfg.MinVersion)
	return nil
}
