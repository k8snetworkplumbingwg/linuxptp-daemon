package cep

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/auth"
)

// Security holds startup-loaded TLS material and the bearer validator. Configure
// it before serving requests. Certificate/trust rotation requires a restart.
type Security struct {
	Config    *auth.AuthConfig
	TLS       *tls.Config
	Validator auth.TokenValidator
}

func NewSecurity(config *auth.AuthConfig, validator auth.TokenValidator) (*Security, WriterFunc, error) {
	if config == nil {
		return nil, nil, fmt.Errorf("authentication configuration is required")
	}
	client, err := auth.NewAuthenticatedClient(config)
	if err != nil {
		return nil, nil, err
	}
	security := &Security{Config: config, Validator: validator}
	if config.IsAuthenticationEnabled() {
		security.TLS, err = config.CreateServerTLSConfig()
		if err != nil {
			return nil, nil, err
		}
	}
	if config.EnableOAuth && validator == nil {
		return nil, nil, fmt.Errorf("OAuth requires a TokenReview validator")
	}
	return security, func(endpoint string) io.Writer {
		return &authenticatedWriter{endpoint: endpoint, client: client}
	}, nil
}

type authenticatedWriter struct {
	endpoint string
	client   *auth.AuthenticatedClient
}

func (w *authenticatedWriter) Write(data []byte) (int, error) {
	response, err := w.client.Post(w.endpoint, data)
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
		if s == nil || s.Config == nil || !s.Config.IsAuthenticationEnabled() || r.URL.Path == apiBase+"health" {
			next.ServeHTTP(w, r)
			return
		}
		// CEPv1 compatibility: loopback is a node boundary with hostNetwork, not a
		// guarantee that the caller runs in this pod. Never trust forwarding headers.
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		if s.Config.EnableMTLS && (r.TLS == nil || len(r.TLS.VerifiedChains) == 0) {
			http.Error(w, "Client certificate required", http.StatusUnauthorized)
			return
		}
		if s.Config.EnableOAuth {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") || s.Validator == nil {
				http.Error(w, "Bearer token required", http.StatusUnauthorized)
				return
			}
			info, err := s.Validator.ValidateToken(r.Context(), strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), s.Config.RequiredAudiences)
			if err != nil || info == nil {
				http.Error(w, "Invalid bearer token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
