// Package socket provides a reconnectable Unix socket wrapper for reliable
// communication with the cloud-event-proxy sidecar.
package socket

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
)

const (
	// DefaultSocketPath is the default path for the cloud-native events socket
	DefaultSocketPath = "/cloud-native/events.sock"
	// DefaultRetryInterval is the default interval between reconnection attempts
	DefaultRetryInterval = 1 * time.Second
	// DefaultMaxRetries is the default maximum number of consecutive retries before logging
	DefaultMaxRetries = 5
)

// ReconnectableSocket provides a thread-safe, auto-reconnecting Unix socket connection.
// It automatically handles broken pipe errors and other connection failures by
// re-establishing the connection.
type ReconnectableSocket struct {
	socketPath    string
	retryInterval time.Duration
	maxRetries    int

	mu         sync.Mutex
	conn       net.Conn
	closed     bool
	retryCount int
}

// Option is a functional option for configuring ReconnectableSocket
type Option func(*ReconnectableSocket)

// WithSocketPath sets a custom socket path
func WithSocketPath(path string) Option {
	return func(rs *ReconnectableSocket) {
		rs.socketPath = path
	}
}

// WithRetryInterval sets a custom retry interval
func WithRetryInterval(interval time.Duration) Option {
	return func(rs *ReconnectableSocket) {
		rs.retryInterval = interval
	}
}

// WithMaxRetries sets the maximum number of retries before logging
func WithMaxRetries(maxRetries int) Option {
	return func(rs *ReconnectableSocket) {
		rs.maxRetries = maxRetries
	}
}

// NewReconnectableSocket creates a new ReconnectableSocket with the given options.
// It does not establish the connection immediately; the connection is established
// lazily on the first Write call.
func NewReconnectableSocket(opts ...Option) *ReconnectableSocket {
	rs := &ReconnectableSocket{
		socketPath:    DefaultSocketPath,
		retryInterval: DefaultRetryInterval,
		maxRetries:    DefaultMaxRetries,
	}

	for _, opt := range opts {
		opt(rs)
	}

	return rs
}

// connect establishes a connection to the Unix socket.
// Must be called with mu held.
func (rs *ReconnectableSocket) connect() error {
	if rs.closed {
		return errors.New("socket is closed")
	}

	// Close existing connection if any
	if rs.conn != nil {
		rs.conn.Close()
		rs.conn = nil
	}

	conn, err := net.Dial("unix", rs.socketPath)
	if err != nil {
		return err
	}

	rs.conn = conn
	rs.retryCount = 0
	glog.Infof("Successfully connected to event socket: %s", rs.socketPath)
	return nil
}

// reconnect attempts to reconnect with retry logic.
// Must be called with mu held.
func (rs *ReconnectableSocket) reconnect() error {
	for {
		if rs.closed {
			return errors.New("socket is closed")
		}

		err := rs.connect()
		if err == nil {
			return nil
		}

		rs.retryCount++
		// Only log periodically to avoid spam
		if rs.retryCount == 1 || rs.retryCount%rs.maxRetries == 0 {
			glog.Errorf("Failed to connect to event socket %s (attempt %d): %v",
				rs.socketPath, rs.retryCount, err)
		}

		time.Sleep(rs.retryInterval)
	}
}

// isBrokenPipe checks if the error indicates a broken pipe or connection issue
func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	// Check for common connection errors
	if errors.Is(err, syscall.EPIPE) || // Broken pipe
		errors.Is(err, syscall.ECONNRESET) || // Connection reset by peer
		errors.Is(err, syscall.ECONNREFUSED) || // Connection refused
		errors.Is(err, syscall.ENOTCONN) { // Not connected
		return true
	}

	// Check for net.OpError wrapping these errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return isBrokenPipe(opErr.Err)
	}

	return false
}

// Write writes data to the socket, automatically reconnecting if necessary.
// This method is thread-safe.
func (rs *ReconnectableSocket) Write(data []byte) (int, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if rs.closed {
		return 0, errors.New("socket is closed")
	}

	// Ensure we have a connection
	if rs.conn == nil {
		if err := rs.reconnect(); err != nil {
			return 0, err
		}
	}

	// Try to write
	n, err := rs.conn.Write(data)
	if err != nil {
		if isBrokenPipe(err) {
			glog.Warningf("Broken pipe detected, attempting to reconnect...")
			// Connection is broken, try to reconnect and retry
			if reconnErr := rs.reconnect(); reconnErr != nil {
				return 0, reconnErr
			}
			// Retry the write after reconnection
			n, err = rs.conn.Write(data)
			if err != nil {
				glog.Errorf("Write failed after reconnection: %v", err)
				return n, err
			}
		} else {
			return n, err
		}
	}

	return n, nil
}

// Close closes the socket connection.
// This method is thread-safe.
func (rs *ReconnectableSocket) Close() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.closed = true
	if rs.conn != nil {
		err := rs.conn.Close()
		rs.conn = nil
		return err
	}
	return nil
}

// IsConnected returns whether the socket is currently connected.
// This method is thread-safe.
func (rs *ReconnectableSocket) IsConnected() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.conn != nil && !rs.closed
}

// GetConnection returns the underlying connection for compatibility.
// Note: Using this bypasses the reconnection logic; prefer using Write directly.
// This method is thread-safe.
func (rs *ReconnectableSocket) GetConnection() net.Conn {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.conn
}
