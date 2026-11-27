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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedirectOAuthAuthorization(t *testing.T) {
	tokenPath := writeFile(t, "token", "original-token")
	cfg := &Config{
		ServerConfig:         &ServerConfig{EnableOAuth: true, ServiceAccountToken: tokenPath},
		AllowedCallbackHosts: []string{"consumer-a.example", "consumer-b.example"},
	}
	ac, err := NewAuthenticatedClient(cfg)
	require.NoError(t, err)
	initial, err := http.NewRequest(http.MethodPost, "https://consumer-a.example/events", nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tokenPath, []byte("rotated-token"), 0600))

	for _, tc := range []struct {
		url  string
		want string
	}{
		{"https://consumer-b.example/events", ""},
		{"https://consumer-a.example/events", ""},
		{"http://consumer-b.example/events", ""},
		{"https://unlisted.example/events", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, tc.url, nil)
			require.NoError(t, err)
			err = ac.CheckRedirect(req, []*http.Request{initial})
			if tc.url != initial.URL.String() {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, req.Header.Get("Authorization"))
		})
	}
}

func TestNewAuthenticatedClient_NilConfig(t *testing.T) {
	ac, err := NewAuthenticatedClient(nil)
	require.NoError(t, err)
	require.NotNil(t, ac)
	assert.NotNil(t, ac)
}

func TestNewAuthenticatedClientWithTimeout(t *testing.T) {
	const timeout = 2 * time.Second
	ac, err := NewAuthenticatedClientWithTimeout(nil, timeout)
	require.NoError(t, err)
	assert.Equal(t, timeout, ac.Timeout)

	defaultClient, err := NewAuthenticatedClient(nil)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, defaultClient.Timeout)
}

func TestNewAuthenticatedClient_InvalidConfig(t *testing.T) {
	// mTLS enabled but no cert paths => Validate fails.
	cfg := &Config{ServerConfig: &ServerConfig{EnableMTLS: true}}
	_, err := NewAuthenticatedClient(cfg)
	assert.Error(t, err)
}

func TestNewAuthenticatedClient_MTLS(t *testing.T) {
	cert, key, ca := writeTestCerts(t)
	cfg := &Config{
		ServerConfig:   &ServerConfig{EnableMTLS: true, CACertPath: ca},
		ClientCertPath: cert,
		ClientKeyPath:  key,
	}
	ac, err := NewAuthenticatedClient(cfg)
	require.NoError(t, err)
	tr, ok := ac.Transport.(*AuthenticatedTransport)
	require.True(t, ok)
	assert.NotNil(t, tr.TLSClientConfig)
}

func TestNewAuthenticatedClient_OAuth(t *testing.T) {
	tok := writeFile(t, "token", "  the-token\n")
	cfg := &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: tok}}
	ac, err := NewAuthenticatedClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, ac.CheckRedirect)
}

func TestNewAuthenticatedClient_OAuthMissingToken(t *testing.T) {
	cfg := &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: "/nope/token"}}
	_, err := NewAuthenticatedClient(cfg)
	assert.Error(t, err)
}

func TestDo_OAuthRejectsCleartext(t *testing.T) {
	tok := writeFile(t, "token", "the-token")
	cfg := &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: tok}}
	ac, err := NewAuthenticatedClient(cfg)
	require.NoError(t, err)

	req, _ := http.NewRequest("GET", "http://insecure.example.com/x", nil)
	_, err = ac.Do(req)
	assert.Error(t, err, "bearer token must not be sent over cleartext http")
}

func TestDo_OAuthSetsBearerOverHTTPS(t *testing.T) {
	var gotAuth string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tok := writeFile(t, "token", "  the-token\n")
	cfg := &Config{ServerConfig: &ServerConfig{EnableOAuth: true, ServiceAccountToken: tok}}
	ac, err := NewAuthenticatedClient(cfg)
	require.NoError(t, err)
	// Trust the test server's TLS cert (same-package access to the unexported client).
	ac.Transport.(*AuthenticatedTransport).Transport = ts.Client().Transport.(*http.Transport).Clone()

	require.NoError(t, os.WriteFile(tok, []byte("rotated-token"), 0600))
	resp, err := ac.Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "Bearer rotated-token", gotAuth)
}

func TestHTTPMethods_NoAuth(t *testing.T) {
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ac, err := NewAuthenticatedClient(nil)
	require.NoError(t, err)

	for _, call := range []func() (*http.Response, error){
		func() (*http.Response, error) { return ac.Get(ts.URL) },
		func() (*http.Response, error) { return ac.Post(ts.URL, "application/json", strings.NewReader(`{}`)) },
		func() (*http.Response, error) {
			req, err := http.NewRequest(http.MethodPut, ts.URL, nil)
			if err != nil {
				return nil, err
			}
			return ac.Do(req)
		},
		func() (*http.Response, error) {
			req, err := http.NewRequest(http.MethodDelete, ts.URL, nil)
			if err != nil {
				return nil, err
			}
			return ac.Do(req)
		},
	} {
		resp, err := call()
		require.NoError(t, err)
		resp.Body.Close()
	}
	assert.Equal(t, []string{"GET", "POST", "PUT", "DELETE"}, seen)
}
