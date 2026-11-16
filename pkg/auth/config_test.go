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

//go:build unittests
// +build unittests

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestCerts generates a self-signed certificate usable as both a leaf
// (client) certificate and its own CA, writing cert/key/ca PEM files into a
// per-test temp dir. It returns their paths.
func writeTestCerts(t *testing.T) (certPath, keyPath, caPath string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-auth"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	caPath = filepath.Join(dir, "ca.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	require.NoError(t, os.WriteFile(caPath, certPEM, 0o600)) // self-signed => cert is its own CA
	return certPath, keyPath, caPath
}

// writeFile writes content to a temp file and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestLoadAuthConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadAuthConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.ServerConfig)
	assert.False(t, cfg.IsAuthenticationEnabled())
}

func TestLoadAuthConfig_NotFound(t *testing.T) {
	_, err := LoadAuthConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	assert.Error(t, err)
}

func TestLoadAuthConfig_InvalidJSON(t *testing.T) {
	p := writeFile(t, "bad.json", "{ not valid json ")
	_, err := LoadAuthConfig(p)
	assert.Error(t, err)
}

func TestLoadAuthConfig_Valid(t *testing.T) {
	p := writeFile(t, "config.json", `{
		"enableMTLS": true,
		"enableOAuth": true,
		"caCertPath": "/etc/ca.crt",
		"clientCertPath": "/etc/client.crt",
		"clientKeyPath": "/etc/client.key",
		"serviceAccountToken": "/var/run/token",
		"requiredAudiences": ["https://kubernetes.default.svc"]
	}`)
	cfg, err := LoadAuthConfig(p)
	require.NoError(t, err)
	assert.True(t, cfg.EnableMTLS)
	assert.True(t, cfg.EnableOAuth)
	assert.Equal(t, "/etc/client.crt", cfg.ClientCertPath)
	assert.Equal(t, "/etc/client.key", cfg.ClientKeyPath)
	assert.Equal(t, []string{"https://kubernetes.default.svc"}, cfg.RequiredAudiences)
}

func TestValidate_NoAuth(t *testing.T) {
	cfg := &Config{ServerConfig: &ServerConfig{}}
	assert.NoError(t, cfg.Validate())
}

func TestValidateCallbackEndpoint(t *testing.T) {
	cfg := &Config{
		ServerConfig:         &ServerConfig{EnableOAuth: true},
		AllowedCallbackHosts: []string{"consumer.example", "*.cloud-events.svc.cluster.local"},
	}
	for _, endpoint := range []string{
		"https://consumer.example/events",
		"https://callback.cloud-events.svc.cluster.local/events",
	} {
		assert.NoError(t, cfg.ValidateCallbackEndpoint(endpoint))
	}
	for _, endpoint := range []string{
		"http://consumer.example/events",
		"https://attacker.example/events",
		"https://cloud-events.svc.cluster.local/events",
	} {
		assert.Error(t, cfg.ValidateCallbackEndpoint(endpoint))
	}

	cfg.AllowedCallbackHosts = []string{"bad*pattern"}
	assert.Error(t, cfg.ValidateCallbackHosts())
}

func TestValidate_MTLSMissingFields(t *testing.T) {
	// mTLS on but no client cert path.
	cfg := &Config{ServerConfig: &ServerConfig{EnableMTLS: true}}
	assert.Error(t, cfg.Validate())

	// client cert set, key missing.
	cfg = &Config{ServerConfig: &ServerConfig{EnableMTLS: true}, ClientCertPath: "/x"}
	assert.Error(t, cfg.Validate())

	// client cert+key set, CA missing.
	cfg = &Config{ServerConfig: &ServerConfig{EnableMTLS: true}, ClientCertPath: "/x", ClientKeyPath: "/y"}
	assert.Error(t, cfg.Validate())
}

func TestValidate_MTLSFileMissing(t *testing.T) {
	// Paths set but files do not exist on disk.
	cfg := &Config{
		ServerConfig:   &ServerConfig{EnableMTLS: true, CACertPath: "/nope/ca.crt"},
		ClientCertPath: "/nope/client.crt",
		ClientKeyPath:  "/nope/client.key",
	}
	assert.Error(t, cfg.Validate())
}

func TestValidate_MTLSOK(t *testing.T) {
	cert, key, ca := writeTestCerts(t)
	cfg := &Config{
		ServerConfig:   &ServerConfig{EnableMTLS: true, CACertPath: ca},
		ClientCertPath: cert,
		ClientKeyPath:  key,
	}
	assert.NoError(t, cfg.Validate())
}

func TestValidate_OAuth(t *testing.T) {
	// OAuth on but token path empty.
	cfg := &Config{ServerConfig: &ServerConfig{EnableOAuth: true}}
	assert.Error(t, cfg.Validate())

	// token path set but file missing.
	cfg = &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: "/nope/token"}}
	assert.Error(t, cfg.Validate())

	// token file present.
	tok := writeFile(t, "token", "abc")
	cfg = &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: tok}}
	assert.NoError(t, cfg.Validate())
}

func TestCreateTLSConfig_Disabled(t *testing.T) {
	cfg := &Config{ServerConfig: &ServerConfig{}}
	tlsCfg, err := cfg.CreateTLSConfig()
	require.NoError(t, err)
	assert.Nil(t, tlsCfg)
}

func TestCreateTLSConfig_OK(t *testing.T) {
	cert, key, ca := writeTestCerts(t)
	cfg := &Config{
		ServerConfig:   &ServerConfig{EnableMTLS: true, CACertPath: ca},
		ClientCertPath: cert,
		ClientKeyPath:  key,
	}
	tlsCfg, err := cfg.CreateTLSConfig()
	require.NoError(t, err)
	require.NotNil(t, tlsCfg)
	assert.Len(t, tlsCfg.Certificates, 1)
	assert.NotNil(t, tlsCfg.RootCAs)
	assert.GreaterOrEqual(t, int(tlsCfg.MinVersion), 0x0303) // >= TLS 1.2
}

func TestCreateTLSConfig_BadCert(t *testing.T) {
	cfg := &Config{
		ServerConfig:   &ServerConfig{EnableMTLS: true, CACertPath: "/nope/ca"},
		ClientCertPath: "/nope/cert",
		ClientKeyPath:  "/nope/key",
	}
	_, err := cfg.CreateTLSConfig()
	assert.Error(t, err)
}

func TestOAuthOnlyServerTrust(t *testing.T) {
	_, _, ca := writeTestCerts(t)
	cfg := &Config{ServerConfig: &ServerConfig{EnableOAuth: true, CACertPath: ca, TLSMinVersion: "VersionTLS13"}}
	tlsCfg, err := cfg.CreateTLSConfig()
	require.NoError(t, err)
	assert.NotNil(t, tlsCfg.RootCAs)
	assert.Empty(t, tlsCfg.Certificates)
	assert.EqualValues(t, 0x0304, tlsCfg.MinVersion)
}

func TestCreateTLSConfig_BadCA(t *testing.T) {
	cert, key, _ := writeTestCerts(t)
	badCA := writeFile(t, "ca.pem", "not a pem")
	cfg := &Config{
		ServerConfig:   &ServerConfig{EnableMTLS: true, CACertPath: badCA},
		ClientCertPath: cert,
		ClientKeyPath:  key,
	}
	_, err := cfg.CreateTLSConfig()
	assert.Error(t, err)
}

func TestGetOAuthToken(t *testing.T) {
	// Disabled => empty, no error.
	cfg := &Config{ServerConfig: &ServerConfig{}}
	tok, err := cfg.GetOAuthToken()
	require.NoError(t, err)
	assert.Empty(t, tok)

	// Enabled with token file.
	p := writeFile(t, "token", "secret-token")
	cfg = &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: p}}
	tok, err = cfg.GetOAuthToken()
	require.NoError(t, err)
	assert.Equal(t, "secret-token", tok)

	// Enabled but file missing => error.
	cfg = &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: "/nope/token"}}
	_, err = cfg.GetOAuthToken()
	assert.Error(t, err)
}

func TestIsAuthenticationEnabled(t *testing.T) {
	assert.False(t, (&Config{ServerConfig: &ServerConfig{}}).IsAuthenticationEnabled())
	assert.True(t, (&Config{ServerConfig: &ServerConfig{EnableMTLS: true}}).IsAuthenticationEnabled())
	assert.True(t, (&Config{ServerConfig: &ServerConfig{EnableOAuth: true}}).IsAuthenticationEnabled())
}

func TestGetConfigSummary(t *testing.T) {
	cfg := &Config{
		ServerConfig: &ServerConfig{
			EnableMTLS:          true,
			EnableOAuth:         true,
			CACertPath:          "/etc/ca.crt",
			ServiceAccountName:  "consumer-sa",
			ServiceAccountToken: "/var/run/token",
		},
		ClientCertPath: "/etc/client.crt",
		ClientKeyPath:  "/etc/client.key",
	}
	s := cfg.GetConfigSummary()
	assert.True(t, strings.Contains(s, "mTLS: true"))
	assert.True(t, strings.Contains(s, "/etc/client.crt"))
	assert.True(t, strings.Contains(s, "OAuth: true"))
	assert.True(t, strings.Contains(s, "consumer-sa"))

	// Both disabled: no detail lines.
	off := (&Config{ServerConfig: &ServerConfig{}}).GetConfigSummary()
	assert.True(t, strings.Contains(off, "mTLS: false"))
	assert.True(t, strings.Contains(off, "OAuth: false"))
}
