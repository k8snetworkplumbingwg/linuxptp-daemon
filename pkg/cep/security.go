package cep

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/auth"
)

// Security holds the listener configuration and bearer validator.
type Security struct {
	Config    *auth.Config
	TLS       *tls.Config
	Validator auth.TokenValidator
	material  *tlsMaterial
}

// NewSecurity creates inbound authentication and an outbound callback writer
// with the supplied callback request timeout.
func NewSecurity(config *auth.Config, validator auth.TokenValidator, callbackTimeout time.Duration) (*Security, WriterFunc, error) {
	if config == nil {
		return nil, nil, fmt.Errorf("authentication configuration is required")
	}
	security := &Security{Config: config, Validator: validator}
	if config.IsAuthenticationEnabled() {
		if err := config.ValidateCallbackHosts(); err != nil {
			return nil, nil, err
		}
		security.material = &tlsMaterial{config: config, timeout: callbackTimeout}
		if err := security.material.reload(); err != nil {
			return nil, nil, err
		}
		security.TLS = &tls.Config{GetConfigForClient: security.material.serverConfig}
	}
	if config.EnableOAuth && validator == nil {
		return nil, nil, fmt.Errorf("OAuth requires a TokenReview validator")
	}
	var plainClient *auth.AuthenticatedClient
	if security.material == nil {
		var err error
		plainClient, err = auth.NewAuthenticatedClientWithTimeout(config, callbackTimeout)
		if err != nil {
			return nil, nil, err
		}
	}
	return security, func(endpoint string) io.Writer {
		return &authenticatedWriter{endpoint: endpoint, client: plainClient, security: security}
	}, nil
}

type authenticatedWriter struct {
	endpoint string
	client   *auth.AuthenticatedClient
	security *Security
}

func (w *authenticatedWriter) Write(data []byte) (int, error) {
	if err := w.security.Config.ValidateCallbackEndpoint(w.endpoint); err != nil {
		return 0, err
	}
	client := w.client
	if w.security.material != nil {
		snapshot := w.security.material.current.Load()
		if err := checkLeafTime(snapshot.clientLeaf); err != nil {
			return 0, err
		}
		client = snapshot.client
		// Close any connections opened by a request racing with rotation.
		defer func() {
			if w.security.material.current.Load() != snapshot {
				client.GetClient().CloseIdleConnections()
			}
		}()
	}
	response, err := client.Post(w.endpoint, data)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("callback returned HTTP %d", response.StatusCode)
	}
	return len(data), nil
}

func (s *Security) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.Config == nil || !s.Config.IsAuthenticationEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		// Only the minimal GET probe is exempt. With hostNetwork, loopback is
		// shared with host processes and other workloads, not a caller identity.
		if r.Method == http.MethodGet && r.URL.Path == apiBase+"health" {
			next.ServeHTTP(w, r)
			return
		}
		if s.Config.EnableMTLS && (r.TLS == nil || len(r.TLS.VerifiedChains) == 0) {
			http.Error(w, "Client certificate required", http.StatusUnauthorized)
			return
		}
		if s.Config.EnableOAuth {
			scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || s.Validator == nil {
				http.Error(w, "Bearer token required", http.StatusUnauthorized)
				return
			}
			info, err := s.Validator.ValidateToken(r.Context(), strings.TrimSpace(token), s.Config.RequiredAudiences)
			if err != nil || info == nil {
				http.Error(w, "Invalid bearer token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
