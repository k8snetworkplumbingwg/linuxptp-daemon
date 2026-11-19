package cep

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/auth"
)

const tlsReloadInterval = 5 * time.Second

type tlsSnapshot struct {
	digests    [5][sha256.Size]byte
	server     *tls.Config
	client     *auth.AuthenticatedClient
	serverLeaf *x509.Certificate
	clientLeaf *x509.Certificate
}

type tlsMaterial struct {
	config  *auth.Config
	timeout time.Duration
	current atomic.Pointer[tlsSnapshot]
}

func (m *tlsMaterial) digests() ([5][sha256.Size]byte, error) {
	cfg := m.config
	paths := [5]string{cfg.CACertPath, cfg.ServerCertPath, cfg.ServerKeyPath}
	if cfg.EnableMTLS {
		paths[3], paths[4] = cfg.ClientCertPath, cfg.ClientKeyPath
	}
	var result [5][sha256.Size]byte
	for i, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return result, fmt.Errorf("read TLS material: %w", err)
		}
		result[i] = sha256.Sum256(data)
	}
	return result, nil
}

// reload is called by a single writer, first during startup and then by the
// listener's polling goroutine. Readers use immutable configuration snapshots.
func (m *tlsMaterial) reload() error {
	before, err := m.digests()
	if err != nil {
		return err
	}
	old := m.current.Load()
	if old != nil && old.digests == before {
		return nil
	}
	server, err := m.config.CreateServerTLSConfig()
	if err != nil {
		return err
	}
	if m.config.EnableMTLS && server.ClientCAs == nil {
		return fmt.Errorf("mTLS requires an inbound client CA pool")
	}
	serverLeaf := server.Certificates[0].Leaf
	if leafErr := checkLeaf(serverLeaf, x509.ExtKeyUsageServerAuth); leafErr != nil {
		return fmt.Errorf("serving certificate: %w", leafErr)
	}
	// A new handshake must use the current trust pool, including after a CA is removed.
	server.SessionTicketsDisabled = true
	client, err := auth.NewAuthenticatedClientWithTimeout(m.config, m.timeout)
	if err != nil {
		return err
	}
	var clientLeaf *x509.Certificate
	if m.config.EnableMTLS {
		transport := client.GetClient().Transport.(*http.Transport)
		clientLeaf = transport.TLSClientConfig.Certificates[0].Leaf
		if leafErr := checkLeaf(clientLeaf, x509.ExtKeyUsageClientAuth); leafErr != nil {
			return fmt.Errorf("callback client certificate: %w", leafErr)
		}
	}
	// Kubernetes may swap the projected volume symlinks during a read.
	after, err := m.digests()
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("TLS files changed during reload")
	}
	m.current.Store(&tlsSnapshot{
		digests: after, server: server, client: client,
		serverLeaf: serverLeaf, clientLeaf: clientLeaf,
	})
	if old != nil {
		old.client.GetClient().CloseIdleConnections()
		glog.Info("Event API TLS material reloaded")
	}
	return nil
}

func (m *tlsMaterial) serverConfig(*tls.ClientHelloInfo) (*tls.Config, error) {
	snapshot := m.current.Load()
	if err := checkLeafTime(snapshot.serverLeaf); err != nil {
		return nil, err
	}
	return snapshot.server, nil
}

func checkLeaf(leaf *x509.Certificate, usage x509.ExtKeyUsage) error {
	if leaf == nil {
		return fmt.Errorf("missing parsed leaf certificate")
	}
	if err := checkLeafTime(leaf); err != nil {
		return err
	}
	if len(leaf.ExtKeyUsage) == 0 && len(leaf.UnknownExtKeyUsage) == 0 {
		return nil
	}
	for _, allowed := range leaf.ExtKeyUsage {
		if allowed == usage || allowed == x509.ExtKeyUsageAny {
			return nil
		}
	}
	return fmt.Errorf("certificate extended key usage does not allow its TLS role")
}

func checkLeafTime(leaf *x509.Certificate) error {
	if leaf != nil {
		now := time.Now()
		if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
			return fmt.Errorf("TLS certificate is outside its validity period")
		}
	}
	return nil
}

func (m *tlsMaterial) watch(ctx context.Context) {
	ticker := time.NewTicker(tlsReloadInterval)
	defer ticker.Stop()
	var lastError string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.reload(); err != nil {
				if err.Error() != lastError {
					glog.Warningf("Event API TLS reload failed; retaining previous configuration: %v", err)
				}
				lastError = err.Error()
			} else {
				lastError = ""
			}
		}
	}
}
