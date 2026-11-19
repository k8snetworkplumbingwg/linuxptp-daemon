package cep

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/auth"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

func securityTestConfig(t *testing.T) *auth.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	cert.DNSNames = []string{"ptp-event-publisher-service-worker-a.openshift-ptp.svc.cluster.local"}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	tokenPath := filepath.Join(dir, "token")
	for path, data := range map[string][]byte{certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPath: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), tokenPath: []byte("first")} {
		if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	cfg := &auth.Config{ServerConfig: &auth.ServerConfig{EnableMTLS: true, EnableOAuth: true, CACertPath: certPath, ServerCertPath: certPath, ServerKeyPath: keyPath, ServiceAccountToken: tokenPath}, ClientCertPath: certPath, ClientKeyPath: keyPath, AllowedCallbackHosts: []string{"127.0.0.1"}}
	return cfg
}

func TestLiveTLSReload(t *testing.T) {
	cfg := securityTestConfig(t)
	cfg.RequiredAudiences = []string{"event-api"}
	if err := os.WriteFile(cfg.ServiceAccountToken, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	security, writer, setupErr := NewSecurity(cfg, testValidator{true}, 2*time.Second)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	old := security.material.current.Load()
	server := httptest.NewUnstartedServer(security.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	server.TLS = security.TLS
	server.StartTLS()
	defer server.Close()

	callback := writer(server.URL)
	if _, err := callback.Write([]byte("{}")); err != nil {
		t.Fatalf("initial secured request: %v", err)
	}
	replace := func(dst, src string) {
		t.Helper()
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if writeErr := os.WriteFile(dst, data, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	next := securityTestConfig(t)
	validCA, readErr := os.ReadFile(cfg.CACertPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := os.WriteFile(cfg.CACertPath, append(validCA, []byte("-----BEGIN CERTIFICATE-----\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := security.material.reload(); err == nil || security.material.current.Load() != old {
		t.Fatal("accepted a partially valid CA bundle")
	}
	if err := os.WriteFile(cfg.CACertPath, validCA, 0600); err != nil {
		t.Fatal(err)
	}
	replace(cfg.ServerCertPath, next.ServerCertPath)
	if err := security.material.reload(); err == nil {
		t.Fatal("accepted a certificate without its matching key")
	}
	if security.material.current.Load() != old {
		t.Fatal("failed rotation replaced active TLS material")
	}
	if _, err := callback.Write([]byte("{}")); err != nil {
		t.Fatalf("request during incomplete rotation: %v", err)
	}

	replace(cfg.ServerKeyPath, next.ServerKeyPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go security.material.watch(ctx)
	deadline := time.Now().Add(2 * tlsReloadInterval)
	for security.material.current.Load() == old && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	current := security.material.current.Load()
	if current == old {
		t.Fatal("watcher did not activate rotated TLS material")
	}
	if slices.Equal(old.serverLeaf.Raw, current.serverLeaf.Raw) ||
		slices.Equal(old.clientLeaf.Raw, current.clientLeaf.Raw) {
		t.Fatal("rotation did not replace both leaf certificates")
	}
	if _, err := callback.Write([]byte("{}")); err != nil {
		t.Fatalf("secured request after rotation on the original listener and writer: %v", err)
	}
	old.client.GetClient().CloseIdleConnections()
	if _, err := (&authenticatedWriter{endpoint: server.URL, client: old.client, security: &Security{Config: cfg}}).Write([]byte("{}")); err == nil {
		t.Fatal("old credentials unexpectedly succeeded after rotation")
	}
}

func TestSecuredCallbackTLSAndTokenRotation(t *testing.T) {
	cfg := securityTestConfig(t)
	security, writer, err := NewSecurity(cfg, testValidator{true}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 2)
	callback := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) == 0 {
			t.Error("callback missing verified client certificate")
		}
		received <- r.Header.Get("Authorization")
		w.WriteHeader(204)
	}))
	callback.TLS = security.TLS.Clone()
	callback.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	callback.StartTLS()
	defer callback.Close()
	for _, token := range []string{"first", "rotated"} {
		if writeErr := os.WriteFile(cfg.ServiceAccountToken, []byte(token), 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, writeErr := writer(callback.URL).Write([]byte(`{}`)); writeErr != nil {
			t.Fatal(writeErr)
		}
		if got := <-received; got != "Bearer "+token {
			t.Fatalf("token header %q", got)
		}
	}
	if _, writeErr := writer("http://example.invalid/callback").Write([]byte(`{}`)); writeErr == nil {
		t.Fatal("allowed plaintext credentials")
	}
	if _, writeErr := writer("https://attacker.example/callback").Write([]byte(`{}`)); writeErr == nil {
		t.Fatal("allowed credentials to an untrusted callback host")
	}
}

func TestNodeServingHostnameVerification(t *testing.T) {
	cfg := securityTestConfig(t)
	serverTLS, err := cfg.CreateServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	for _, node := range []string{"worker-a", "worker-b"} {
		clientTLS, tlsErr := cfg.CreateTLSConfig()
		if tlsErr != nil {
			t.Fatal(tlsErr)
		}
		clientTLS.ServerName = "ptp-event-publisher-service-" + node + ".openshift-ptp.svc.cluster.local"
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
		resp, requestErr := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
		client.CloseIdleConnections()
		if node == "worker-a" && requestErr != nil {
			t.Fatalf("matching node: %v", requestErr)
		}
		if node == "worker-b" && requestErr == nil {
			t.Fatal("accepted another node's serving certificate")
		}
	}
}

// Exercise the actual handlers and PubSub writer over TLS, including initial
// notification and later push, over real loopback connections.
func TestSecureSubscribePullPush(t *testing.T) {
	cfg := securityTestConfig(t)
	cfg.RequiredAudiences = []string{"event-api"}
	if writeErr := os.WriteFile(cfg.ServiceAccountToken, []byte("test-token"), 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	security, writer, err := NewSecurity(cfg, testValidator{true}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pushes := make(chan []byte, 2)
	callback := httptest.NewUnstartedServer(security.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		pushes <- body
		w.WriteHeader(http.StatusNoContent)
	})))
	callback.TLS = security.TLS.Clone()
	callback.StartTLS()
	defer callback.Close()
	store, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	cache := NewEventCache("worker-0")
	ps := NewPubSub(store, writer)
	proxy := NewCloudEventProxy(cache, ps, "worker-0", "https://publisher.example:9043")
	proxy.Security = security
	api := httptest.NewUnstartedServer(proxy.Handler())
	api.TLS = security.TLS.Clone()
	api.StartTLS()
	defer api.Close()
	message := ipc.Message{Version: ipc.Version, Type: ipc.TypePTPState, Profile: "test", IFace: "eth0", Values: ipc.StateValue{State: ipc.StateLocked}}
	cache.Update(message)
	const resource = "/cluster/node/worker-0/sync/ptp-status/lock-state"
	client, err := auth.NewAuthenticatedClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(subscriptionRequest{EndpointURI: callback.URL + "/event", Resource: resource})
	response, err := client.Post(api.URL+apiBase+"subscriptions", body)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("subscribe HTTP %d", response.StatusCode)
	}
	checkPush := func() {
		t.Helper()
		select {
		case data := <-pushes:
			if !strings.Contains(string(data), resource) {
				t.Fatalf("unexpected event: %s", data)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no authenticated notification received")
		}
	}
	checkPush()
	response, err = client.Get(api.URL + apiBase + strings.TrimPrefix(resource, "/") + "/CurrentState")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("pull HTTP %d", response.StatusCode)
	}
	message.Values = ipc.StateValue{State: ipc.StateFreerun}
	updated := cache.Update(message)
	if updated == nil {
		t.Fatal("missing updated event")
	}
	ps.Publish(updated)
	checkPush()

	// Fresh, hostname-verified TLS connections must enforce both credentials
	// even though the API and callback listeners are on loopback.
	for _, tc := range []struct {
		name, method, path, token string
		cert                      bool
		want                      int
	}{
		{"anonymous", http.MethodGet, "subscriptions", "", false, 401},
		{"token only", http.MethodGet, "subscriptions", "test-token", false, 401},
		{"certificate only", http.MethodGet, "subscriptions", "", true, 401},
		{"invalid token", http.MethodGet, "subscriptions", "invalid", true, 401},
		{"authenticated discovery", http.MethodGet, "publishers", "test-token", true, 200},
		{"health", http.MethodGet, "health", "", false, 200},
		{"health POST anonymous", http.MethodPost, "health", "", false, 401},
		{"health POST authenticated", http.MethodPost, "health", "test-token", true, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tlsConfig, tlsErr := cfg.CreateTLSConfig()
			if tlsErr != nil {
				t.Fatal(tlsErr)
			}
			if !tc.cert {
				tlsConfig.Certificates = nil
			}
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			request, requestErr := http.NewRequest(tc.method, api.URL+apiBase+tc.path, nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			testResponse, requestErr := client.Do(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			defer testResponse.Body.Close()
			if testResponse.StatusCode != tc.want {
				t.Fatalf("got HTTP %d want %d", testResponse.StatusCode, tc.want)
			}
		})
	}
}

type testValidator struct{ accepted bool }

func (v testValidator) ValidateToken(_ context.Context, token string, audiences []string) (*auth.TokenInfo, error) {
	if !v.accepted || token != "test-token" || len(audiences) != 1 || audiences[0] != "event-api" {
		return nil, fmt.Errorf("rejected")
	}
	return &auth.TokenInfo{Username: "consumer"}, nil
}

func TestProtectedRouteAuthentication(t *testing.T) {
	for _, remote := range []string{"192.0.2.1:1234", "127.0.0.1:1234", "127.0.0.2:1234", "[::1]:1234", "[::ffff:127.0.0.1]:1234", "malformed"} {
		for _, route := range []string{"subscriptions", "subscriptions/id", "publishers", "cluster/node/test/sync/ptp-status/lock-state/CurrentState"} {
			for _, tc := range []struct {
				name                  string
				mtls, oauth           bool
				cert, token, accepted bool
				want                  int
			}{
				{"neither", true, true, false, false, true, 401},
				{"certificate only", true, true, true, false, true, 401},
				{"token only", true, true, false, true, true, 401},
				{"rejected token", true, true, true, true, false, 401},
				{"both", true, true, true, true, true, 204},
				{"mtls missing", true, false, false, false, true, 401},
				{"mtls valid", true, false, true, false, true, 204},
				{"oauth missing", false, true, false, false, true, 401},
				{"oauth valid", false, true, false, true, true, 204},
				{"disabled", false, false, false, false, false, 204},
			} {
				t.Run(remote+"/"+route+"/"+tc.name, func(t *testing.T) {
					s := &Security{Config: &auth.Config{ServerConfig: &auth.ServerConfig{EnableMTLS: tc.mtls, EnableOAuth: tc.oauth, RequiredAudiences: []string{"event-api"}}}, Validator: testValidator{tc.accepted}}
					request := httptest.NewRequest(http.MethodGet, apiBase+route, nil)
					request.RemoteAddr = remote
					// Neither a caller-controlled host nor forwarding headers grant trust.
					request.Host = "localhost"
					request.Header.Set("X-Forwarded-For", "127.0.0.1")
					request.Header.Set("Forwarded", "for=127.0.0.1")
					if tc.cert {
						request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
					}
					if tc.token {
						request.Header.Set("Authorization", "Bearer test-token")
					}
					response := httptest.NewRecorder()
					s.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(response, request)
					if response.Code != tc.want {
						t.Fatalf("got %d want %d", response.Code, tc.want)
					}
				})
			}
		}
	}
}

func TestAuthenticationExceptions(t *testing.T) {
	s := &Security{Config: &auth.Config{ServerConfig: &auth.ServerConfig{EnableMTLS: true, EnableOAuth: true}}}
	for _, remote := range []string{"192.0.2.1:1234", "127.0.0.1:1234", "[::1]:1234"} {
		for _, tc := range []struct {
			method, path string
			want         int
		}{
			{http.MethodGet, "health", 204},
			{http.MethodHead, "health", 401},
			{http.MethodPost, "health", 401},
			{http.MethodGet, "subscriptions", 401},
			{http.MethodGet, "health/extra", 401},
			{http.MethodGet, "health/", 401},
		} {
			t.Run(remote+"/"+tc.method+"/"+tc.path, func(t *testing.T) {
				r := httptest.NewRequest(tc.method, apiBase+tc.path, nil)
				r.RemoteAddr = remote
				w := httptest.NewRecorder()
				s.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
				if w.Code != tc.want {
					t.Fatalf("got %d want %d", w.Code, tc.want)
				}
			})
		}
	}
}

// Native CEPv2 never exposes legacy registration, injection or debug handlers.
func TestNativeRouteBoundary(t *testing.T) {
	for _, secured := range []bool{false, true} {
		store, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
		if err != nil {
			t.Fatal(err)
		}
		ps := NewPubSub(store, func(string) io.Writer {
			t.Error("unsupported operation attempted a callback")
			return io.Discard
		})
		proxy := NewCloudEventProxy(NewEventCache("worker-0"), ps, "worker-0", "https://publisher.example:9043")
		proxy.Security = &Security{Config: &auth.Config{ServerConfig: &auth.ServerConfig{EnableMTLS: secured, EnableOAuth: secured, RequiredAudiences: []string{"event-api"}}}, Validator: testValidator{true}}
		for _, tc := range []struct {
			method, path string
			want         int
		}{
			{http.MethodGet, "health", 200},
			{http.MethodPost, "health", 405},
			{http.MethodHead, "health", 405},
			{http.MethodGet, "publishers", 200},
			{http.MethodPost, "publishers", 405},
			{http.MethodDelete, "publishers", 405},
			{http.MethodGet, "publishers/id", 404},
			{http.MethodDelete, "publishers/id", 405},
			{http.MethodPut, "subscriptions/status/id", 405},
			{http.MethodPost, "create/event", 405},
			{http.MethodPost, "log", 405},
			{http.MethodPost, "dummy", 405},
			{http.MethodPost, "dummy2", 405},
		} {
			t.Run(fmt.Sprintf("secured=%t/%s/%s", secured, tc.method, tc.path), func(t *testing.T) {
				r := httptest.NewRequest(tc.method, apiBase+tc.path, nil)
				r.RemoteAddr = "127.0.0.1:1234"
				r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
				r.Header.Set("Authorization", "Bearer test-token")
				w := httptest.NewRecorder()
				proxy.Handler().ServeHTTP(w, r)
				if w.Code != tc.want {
					t.Fatalf("got %d want %d", w.Code, tc.want)
				}
				if tc.path == "health" && tc.want == 405 && w.Header().Get("Allow") != http.MethodGet {
					t.Fatal("health must advertise GET as its only allowed method")
				}
			})
		}
	}
}

func TestSecurityFailsClosedOnIncompleteConfiguration(t *testing.T) {
	for _, cfg := range []*auth.Config{nil, {ServerConfig: &auth.ServerConfig{EnableMTLS: true}}, {ServerConfig: &auth.ServerConfig{EnableOAuth: true}}} {
		if _, _, err := NewSecurity(cfg, nil, 2*time.Second); err == nil {
			t.Fatal("accepted incomplete security configuration")
		}
	}
}
