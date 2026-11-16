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

func TestSecuredCallbackTLSAndTokenRotation(t *testing.T) {
	cfg := securityTestConfig(t)
	security, writer, err := NewSecurity(cfg, testValidator{true})
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
// notification and later push, while treating local test sockets as remote peers.
func TestSecureSubscribePullPush(t *testing.T) {
	cfg := securityTestConfig(t)
	cfg.RequiredAudiences = []string{"event-api"}
	if writeErr := os.WriteFile(cfg.ServiceAccountToken, []byte("test-token"), 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	security, writer, err := NewSecurity(cfg, testValidator{true})
	if err != nil {
		t.Fatal(err)
	}
	remote := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = "192.0.2.1:1234"
			next.ServeHTTP(w, r)
		})
	}
	pushes := make(chan []byte, 2)
	callback := httptest.NewUnstartedServer(remote(security.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		pushes <- body
		w.WriteHeader(http.StatusNoContent)
	}))))
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
	api := httptest.NewUnstartedServer(remote(proxy.Handler()))
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

	// A real HTTPS connection with the server CA but no credentials is rejected.
	tlsConfig, err := cfg.CreateTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig.Certificates = nil
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
	defer anonymous.CloseIdleConnections()
	response, err = anonymous.Get(api.URL + apiBase + "subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("anonymous HTTP %d", response.StatusCode)
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
	for _, route := range []string{"subscriptions", "subscriptions/id", "publishers", "cluster/node/test/sync/ptp-status/lock-state/CurrentState"} {
		for _, tc := range []struct {
			name                  string
			cert, token, accepted bool
			want                  int
		}{
			{"neither", false, false, true, 401}, {"certificate only", true, false, true, 401},
			{"token only", false, true, true, 401}, {"rejected token", true, true, false, 401},
			{"both", true, true, true, 204},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				s := &Security{Config: &auth.Config{ServerConfig: &auth.ServerConfig{EnableMTLS: true, EnableOAuth: true, RequiredAudiences: []string{"event-api"}}}, Validator: testValidator{tc.accepted}}
				request := httptest.NewRequest(http.MethodGet, apiBase+route, nil)
				request.RemoteAddr = "192.0.2.1:1234"
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

func TestAuthenticationExceptions(t *testing.T) {
	s := &Security{Config: &auth.Config{ServerConfig: &auth.ServerConfig{EnableMTLS: true, EnableOAuth: true}}}
	for _, tc := range []struct {
		path, remote string
		want         int
	}{
		{apiBase + "health", "192.0.2.1:1234", 204},
		{apiBase + "subscriptions", "127.0.0.1:1234", 204},
		{apiBase + "subscriptions", "[::1]:1234", 204},
		{apiBase + "health/extra", "192.0.2.1:1234", 401},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		s.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.want)
		}
	}
}

func TestSecurityFailsClosedOnIncompleteConfiguration(t *testing.T) {
	for _, cfg := range []*auth.Config{nil, {ServerConfig: &auth.ServerConfig{EnableMTLS: true}}, {ServerConfig: &auth.ServerConfig{EnableOAuth: true}}} {
		if _, _, err := NewSecurity(cfg, nil); err == nil {
			t.Fatal("accepted incomplete security configuration")
		}
	}
}
