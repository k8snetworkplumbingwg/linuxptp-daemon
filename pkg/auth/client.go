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
	"bytes"
	"fmt"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

// AuthenticatedClient wraps an HTTP client with authentication capabilities
type AuthenticatedClient struct {
	client     *http.Client
	authConfig *Config
}

const defaultHTTPClientTimeout = 30 * time.Second

// rejectInsecureRedirect refuses to follow a redirect to a non-HTTPS target so
// that a credential-bearing request cannot be downgraded to cleartext.
func rejectInsecureRedirect(req *http.Request, _ []*http.Request) error {
	if req.URL.Scheme != httpsScheme {
		return fmt.Errorf("refusing to follow redirect to non-HTTPS URL %q", req.URL.Redacted())
	}
	return nil
}

func rejectUnsafeRedirect(config *Config) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if err := rejectInsecureRedirect(req, via); err != nil {
			return err
		}
		if err := config.ValidateCallbackEndpoint(req.URL.String()); err != nil {
			return fmt.Errorf("refusing callback redirect: %w", err)
		}
		return setOAuthAuthorization(config, req)
	}
}

// setOAuthAuthorization reads the current projected token for each request.
func setOAuthAuthorization(config *Config, req *http.Request) error {
	if !config.EnableOAuth {
		return nil
	}
	token, err := config.GetOAuthToken()
	if err != nil {
		return fmt.Errorf("failed to read OAuth token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// NewAuthenticatedClient creates a new authenticated HTTP client
func NewAuthenticatedClient(authConfig *Config) (*AuthenticatedClient, error) {
	return NewAuthenticatedClientWithTimeout(authConfig, defaultHTTPClientTimeout)
}

// NewAuthenticatedClientWithTimeout creates an authenticated HTTP client with
// the caller's request timeout.
func NewAuthenticatedClientWithTimeout(authConfig *Config, timeout time.Duration) (*AuthenticatedClient, error) {
	if authConfig == nil {
		// Return a basic HTTP client if no auth config is provided
		return &AuthenticatedClient{
			client: &http.Client{
				Timeout: timeout,
			},
			// Initialize the embedded Config so the no-auth client is safe
			// to use: Do/IsAuthenticated read promoted fields (EnableOAuth,
			// EnableMTLS) that would nil-panic on an uninitialized embed.
			authConfig: &Config{ServerConfig: &ServerConfig{}},
		}, nil
	}

	// Validate the configuration
	if err := authConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid authentication configuration: %v", err)
	}

	client := &http.Client{
		Timeout: timeout,
	}

	// Both modes use HTTPS; OAuth-only also needs the configured server CA.
	if authConfig.IsAuthenticationEnabled() {
		tlsConfig, err := authConfig.CreateTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS configuration: %v", err)
		}

		client.Transport = &http.Transport{
			TLSClientConfig: tlsConfig,
			MaxIdleConns:    100,
			IdleConnTimeout: 90 * time.Second,
		}
		client.CheckRedirect = rejectUnsafeRedirect(authConfig)
		log.Info("Configured HTTPS client")
	}

	// Verify that the token is readable up front so misconfiguration fails fast.
	if authConfig.EnableOAuth {
		if _, err := authConfig.GetOAuthToken(); err != nil {
			return nil, fmt.Errorf("failed to get OAuth token: %v", err)
		}
		log.Info("Configured HTTP client with OAuth")
	}

	ac := &AuthenticatedClient{
		client:     client,
		authConfig: authConfig,
	}

	log.Info("Created authenticated HTTP client")
	return ac, nil
}

// Do performs an HTTP request with authentication
func (ac *AuthenticatedClient) Do(req *http.Request) (*http.Response, error) {
	if ac.authConfig.IsAuthenticationEnabled() && req.URL.Scheme != httpsScheme {
		return nil, fmt.Errorf("refusing authenticated request to non-HTTPS URL %q", req.URL.Redacted())
	}
	if err := setOAuthAuthorization(ac.authConfig, req); err != nil {
		return nil, err
	}

	// Add content-type if not already set
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return ac.client.Do(req)
}

// Get performs a GET request with authentication
func (ac *AuthenticatedClient) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %v", err)
	}

	return ac.Do(req)
}

// Post performs a POST request with authentication
func (ac *AuthenticatedClient) Post(url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create POST request: %v", err)
	}

	return ac.Do(req)
}

// Put performs a PUT request with authentication
func (ac *AuthenticatedClient) Put(url string) (*http.Response, error) {
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PUT request: %v", err)
	}

	return ac.Do(req)
}

// Delete performs a DELETE request with authentication
func (ac *AuthenticatedClient) Delete(url string) (*http.Response, error) {
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create DELETE request: %v", err)
	}

	return ac.Do(req)
}

// GetClient returns the underlying HTTP client
func (ac *AuthenticatedClient) GetClient() *http.Client {
	return ac.client
}

// GetAuthConfig returns the authentication configuration
func (ac *AuthenticatedClient) GetAuthConfig() *Config {
	return ac.authConfig
}

// RefreshOAuthToken verifies the OAuth token is currently readable from the
// service account token file. The client reloads the token on each request and
// allowed redirect, so callers no longer need to invoke this to pick up a
// rotated token. It lets callers proactively surface a missing or unreadable file.
func (ac *AuthenticatedClient) RefreshOAuthToken() error {
	if !ac.authConfig.EnableOAuth {
		return nil
	}

	if _, err := ac.authConfig.GetOAuthToken(); err != nil {
		return fmt.Errorf("failed to refresh OAuth token: %v", err)
	}

	log.Info("Verified OAuth token is readable")
	return nil
}

// IsAuthenticated returns true if the client is configured with authentication
func (ac *AuthenticatedClient) IsAuthenticated() bool {
	return ac.authConfig.IsAuthenticationEnabled()
}
