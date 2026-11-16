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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Config contains the server and outbound-client authentication settings.
type Config struct {
	*ServerConfig
	ClientCertPath       string   `json:"clientCertPath"`
	ClientKeyPath        string   `json:"clientKeyPath"`
	AllowedCallbackHosts []string `json:"allowedCallbackHosts"`
}

// LoadAuthConfig loads authentication configuration from a JSON file
func LoadAuthConfig(configPath string) (*Config, error) {
	if configPath == "" {
		log.Info("No authentication config path provided, using default (no auth)")
		return &Config{
			ServerConfig: &ServerConfig{},
		}, nil
	}

	// Check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("authentication config file not found: %s", configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read authentication config file %s: %v", configPath, err)
	}

	var config Config
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal authentication config: %v", err)
	}

	// Initialize the embedded server configuration if it is nil.
	if config.ServerConfig == nil {
		config.ServerConfig = &ServerConfig{}
	}

	log.Infof("Loaded authentication config from %s", configPath)
	return &config, nil
}

func (c *Config) validateCallbackHosts() error {
	if !c.IsAuthenticationEnabled() {
		return nil
	}
	if len(c.AllowedCallbackHosts) == 0 {
		return fmt.Errorf("at least one allowed callback host is required when authentication is enabled")
	}
	for _, pattern := range c.AllowedCallbackHosts {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		host := pattern
		if strings.HasPrefix(pattern, "*.") {
			host = strings.TrimPrefix(pattern, "*.")
		}
		if host == "" || strings.ContainsAny(host, "*/: ") || (strings.Contains(pattern, "*") && !strings.HasPrefix(pattern, "*.")) {
			return fmt.Errorf("invalid allowed callback host %q", pattern)
		}
	}
	return nil
}

// ValidateCallbackHosts validates the configured outbound callback allowlist.
func (c *Config) ValidateCallbackHosts() error {
	return c.validateCallbackHosts()
}

// ValidateCallbackEndpoint ensures credentials are sent only to an explicitly
// allowed HTTPS callback host. Wildcards match subdomains only (for example,
// *.svc.cluster.local does not match svc.cluster.local itself).
func (c *Config) ValidateCallbackEndpoint(endpoint string) error {
	if !c.IsAuthenticationEnabled() {
		return nil
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Scheme != httpsScheme || parsed.Hostname() == "" {
		return fmt.Errorf("authenticated callback must use an absolute HTTPS URL")
	}
	host := strings.ToLower(parsed.Hostname())
	for _, pattern := range c.AllowedCallbackHosts {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*.")
			if host != suffix && strings.HasSuffix(host, "."+suffix) {
				return nil
			}
		} else if host == pattern {
			return nil
		}
	}
	return fmt.Errorf("callback host %q is not allowed", host)
}

// Validate validates the authentication configuration
func (c *Config) Validate() error {
	if c == nil || c.ServerConfig == nil {
		return fmt.Errorf("authentication configuration is required")
	}
	if c.EnableMTLS {
		if c.ClientCertPath == "" {
			return fmt.Errorf("client certificate path is required when mTLS is enabled")
		}
		if c.ClientKeyPath == "" {
			return fmt.Errorf("client key path is required when mTLS is enabled")
		}
		if c.CACertPath == "" {
			return fmt.Errorf("CA certificate path is required when mTLS is enabled")
		}

		// Check if certificate files exist
		if _, err := os.Stat(c.ClientCertPath); os.IsNotExist(err) {
			return fmt.Errorf("client certificate file not found: %s", c.ClientCertPath)
		}
		if _, err := os.Stat(c.ClientKeyPath); os.IsNotExist(err) {
			return fmt.Errorf("client key file not found: %s", c.ClientKeyPath)
		}
		if _, err := os.Stat(c.CACertPath); os.IsNotExist(err) {
			return fmt.Errorf("CA certificate file not found: %s", c.CACertPath)
		}
	}

	if c.EnableOAuth {
		if c.ServiceAccountToken == "" {
			return fmt.Errorf("service account token path is required when OAuth is enabled")
		}

		// Check if service account token file exists
		if _, err := os.Stat(c.ServiceAccountToken); os.IsNotExist(err) {
			return fmt.Errorf("service account token file not found: %s", c.ServiceAccountToken)
		}
	}

	return nil
}

// CreateTLSConfig configures server trust for HTTPS and an optional mTLS identity.
func (c *Config) CreateTLSConfig() (*tls.Config, error) {
	if !c.IsAuthenticationEnabled() {
		return nil, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.EnableMTLS {
		cert, err := tls.LoadX509KeyPair(c.ClientCertPath, c.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	if c.CACertPath != "" {
		caCert, err := os.ReadFile(c.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %v", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}
	// Apply the centrally-managed TLS profile (min version + cipher suites)
	// sourced from the cluster TLSSecurityProfile. It may raise MinVersion above
	// the TLS 1.2 floor. No hostname verification is skipped: FQDN / service-DNS
	// connections must present a valid server certificate signed by the
	// configured CA.
	c.ApplyTLSProfile(tlsConfig)

	log.Info("Created TLS client configuration")
	return tlsConfig, nil
}

// CreateServerTLSConfig builds a TLS configuration for an inbound server, such
// as the event-consumer callback endpoint that receives pushed CloudEvents.
// It presents the server certificate (ServerCertPath/ServerKeyPath) and, when a
// CA is configured, verifies a client certificate presented by the peer.
// The handshake permits a missing certificate; middleware must then enforce
// every enabled mechanism on protected remote requests. This also supports
// OAuth-only mode and the loopback exemption. The centrally-managed TLS profile
// (min version + cipher suites from the cluster TLSSecurityProfile) is applied
// on top.
//
// This is the server-role counterpart of CreateTLSConfig: per O-RAN CR-0003
// clause 4.1.1 a callback reachable from a separate POD/VM must be protected by
// an authorization mechanism (clause 3.2 — mTLS and/or OAuth), so the consumer
// callback cannot serve plaintext once authentication is enabled.
func (c *Config) CreateServerTLSConfig() (*tls.Config, error) {
	if c.ServerCertPath == "" || c.ServerKeyPath == "" {
		return nil, fmt.Errorf("server certificate and key paths are required for a TLS callback server")
	}

	cert, err := tls.LoadX509KeyPair(c.ServerCertPath, c.ServerKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	if c.CACertPath != "" {
		var caCert []byte
		if caCert, err = os.ReadFile(c.CACertPath); err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %v", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.ClientCAs = caCertPool
		// Verify presented certificates during the handshake. Middleware enforces
		// mandatory mTLS and/or bearer validation according to the enabled flags.
		//
		// SECURITY CONTRACT: because a missing client certificate is tolerated
		// here, the HTTP handler in front of this listener MUST enforce
		// authentication per request - require a verified client certificate
		// when EnableMTLS and validate the bearer token when EnableOAuth (with a
		// loopback exemption). Serving traffic off this tls.Config without middleware
		// would accept unauthenticated requests. In hostNetwork pods, loopback
		// identifies the node network namespace, not just this pod.
		tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
	}

	c.ApplyTLSProfile(tlsConfig)

	log.Info("Created TLS configuration for callback server")
	return tlsConfig, nil
}

// GetOAuthToken reads the OAuth token from the service account token file
func (c *Config) GetOAuthToken() (string, error) {
	if !c.EnableOAuth {
		return "", nil
	}

	token, err := os.ReadFile(c.ServiceAccountToken)
	if err != nil {
		return "", fmt.Errorf("failed to read service account token: %v", err)
	}

	value := strings.TrimSpace(string(token))
	if value == "" {
		return "", fmt.Errorf("service account token file is empty")
	}
	return value, nil
}

// IsAuthenticationEnabled returns true if either mTLS or OAuth is enabled
func (c *Config) IsAuthenticationEnabled() bool {
	return c.EnableMTLS || c.EnableOAuth
}

// GetConfigSummary returns a summary of the authentication configuration
func (c *Config) GetConfigSummary() string {
	summary := "Authentication Configuration:\n"
	summary += fmt.Sprintf("  mTLS: %t\n", c.EnableMTLS)
	if c.EnableMTLS {
		summary += fmt.Sprintf("    Client Cert: %s\n", c.ClientCertPath)
		summary += fmt.Sprintf("    Client Key: %s\n", c.ClientKeyPath)
		summary += fmt.Sprintf("    CA Cert: %s\n", c.CACertPath)
		summary += fmt.Sprintf("    Use Service CA: %t\n", c.UseServiceCA)
	}
	summary += fmt.Sprintf("  OAuth: %t\n", c.EnableOAuth)
	if c.EnableOAuth {
		summary += fmt.Sprintf("    Service Account: %s\n", c.ServiceAccountName)
		summary += fmt.Sprintf("    Token Path: %s\n", c.ServiceAccountToken)
		summary += fmt.Sprintf("    Use OpenShift OAuth: %t\n", c.UseOpenShiftOAuth)
	}
	return summary
}
