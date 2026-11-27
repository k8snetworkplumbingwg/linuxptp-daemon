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
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	tlsReloadInterval = 5 * time.Second
	callbackCertIndex = 3
	callbackKeyIndex  = 4
)

type tlsSnapshot struct {
	digests    [5][sha256.Size]byte
	server     *tls.Config
	client     *http.Client
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
		paths[callbackCertIndex], paths[callbackKeyIndex] = cfg.ClientCertPath, cfg.ClientKeyPath
	}
	var result [5][sha256.Size]byte
	for i, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			// An unreadable callback keypair disables pushes only.
			if i >= callbackCertIndex {
				continue
			}
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
	var missing [sha256.Size]byte
	callbackKeypairPresent := before[callbackCertIndex] != missing && before[callbackKeyIndex] != missing
	// Retry a present keypair if the outbound client could not be initialized.
	if old != nil && old.digests == before && (old.client != nil || !callbackKeypairPresent) {
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
	var client *http.Client
	var clientLeaf *x509.Certificate
	if !m.config.EnableMTLS || callbackKeypairPresent {
		client, err = auth.NewAuthenticatedClientWithTimeout(m.config, m.timeout)
		if err == nil && m.config.EnableMTLS {
			transport := client.Transport.(*auth.AuthenticatedTransport)
			clientLeaf = transport.TLSClientConfig.Certificates[0].Leaf
			err = checkLeaf(clientLeaf, x509.ExtKeyUsageClientAuth)
		}
		if err != nil {
			if !m.config.EnableMTLS {
				return err
			}
			if old == nil || old.digests != before {
				glog.Warningf("Event callback client unavailable: %v", err)
			}
			client, clientLeaf = nil, nil
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
		if old.client != nil {
			old.client.CloseIdleConnections()
		}
		if old.digests != after || old.client != client {
			glog.Info("Event API TLS material reloaded")
		}
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
	var lastError string
	_ = wait.PollUntilContextCancel(ctx, tlsReloadInterval, false, func(context.Context) (bool, error) {
		if err := m.reload(); err != nil {
			if err.Error() != lastError {
				glog.Warningf("Event API TLS reload failed; retaining previous configuration: %v", err)
			}
			lastError = err.Error()
		} else {
			lastError = ""
		}
		return false, nil
	})
}
