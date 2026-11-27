// Copyright 2025 The Cloud Native Events Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultHTTPClientTimeout = 30 * time.Second

// NewAuthenticatedClient creates a standard HTTP client with the configured authentication.
func NewAuthenticatedClient(cfg *Config) (*http.Client, error) {
	return NewAuthenticatedClientWithTimeout(cfg, defaultHTTPClientTimeout)
}

// NewAuthenticatedClientWithTimeout creates an HTTP client with the caller's request timeout.
func NewAuthenticatedClientWithTimeout(cfg *Config, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{Timeout: timeout}
	if cfg == nil {
		return client, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid authentication configuration: %w", err)
	}
	if !cfg.IsAuthenticationEnabled() {
		return client, nil
	}

	tlsConfig, err := cfg.CreateTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("create client TLS configuration: %w", err)
	}
	if cfg.EnableOAuth {
		if _, err := cfg.GetOAuthToken(); err != nil {
			return nil, fmt.Errorf("read OAuth token: %w", err)
		}
	}
	client.Transport = &AuthenticatedTransport{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
			MaxIdleConns:    100,
			IdleConnTimeout: 90 * time.Second,
		},
		config: cfg,
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if req.URL.Scheme != httpsScheme {
			return fmt.Errorf("refusing redirect to non-HTTPS URL %q", req.URL.Redacted())
		}
		if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("refusing cross-origin authenticated redirect")
		}
		if err := cfg.ValidateCallbackEndpoint(req.URL.String()); err != nil {
			return fmt.Errorf("refusing callback redirect: %w", err)
		}
		return nil
	}
	return client, nil
}

// AuthenticatedTransport applies authentication to requests sent through its HTTPS transport.
type AuthenticatedTransport struct {
	*http.Transport
	config *Config
}

func (t *AuthenticatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != httpsScheme {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, fmt.Errorf("refusing authenticated request to non-HTTPS URL %q", req.URL.Redacted())
	}
	if t.config.EnableOAuth {
		token, err := t.config.GetOAuthToken()
		if err != nil {
			if req.Body != nil {
				req.Body.Close()
			}
			return nil, fmt.Errorf("read OAuth token: %w", err)
		}
		req = req.Clone(req.Context())
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return t.Transport.RoundTrip(req)
}
