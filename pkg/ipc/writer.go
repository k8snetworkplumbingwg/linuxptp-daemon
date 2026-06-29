package ipc

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

const (
	writerDialTimeout  = 5 * time.Second
	writerWriteTimeout = 5 * time.Second
)

// Writer drains a Message channel and writes each message to a Unix socket
// as newline-delimited JSON. It manages its own connection with exponential
// backoff reconnection, independent of the CEPv1 event socket.
type Writer struct {
	socketPath  string
	msgCh       <-chan Message
	closeCh     <-chan bool
	conn        net.Conn
	connMu      sync.Mutex
	reconnectMu sync.Mutex
}

// NewWriter creates a Writer that will connect to socketPath, read messages
// from msgCh, and shut down when closeCh receives a value or is closed.
func NewWriter(socketPath string, msgCh <-chan Message, closeCh <-chan bool) *Writer {
	return &Writer{
		socketPath: socketPath,
		msgCh:      msgCh,
		closeCh:    closeCh,
	}
}

// Run is the blocking entry point — call as a goroutine.
// It establishes a connection to the CEPv2 socket and drains the message channel,
// writing each message as newline-delimited JSON. It retries the connection
// indefinitely until shutdown.
func (w *Writer) Run() {
	defer w.setConn(nil)

	if !w.reconnect() {
		return
	}

	for {
		select {
		case <-w.closeCh:
			return
		case msg := <-w.msgCh:
			w.write(msg)
		}
	}
}

func (w *Writer) getConn() net.Conn {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	return w.conn
}

func (w *Writer) setConn(c net.Conn) {
	w.connMu.Lock()
	old := w.conn
	w.conn = c
	w.connMu.Unlock()
	if old != nil && old != c {
		if err := old.Close(); err != nil {
			glog.Warningf("IPC writer: failed to close old connection: %v", err)
		}
	}
}

// reconnect retries indefinitely until a connection is established or closeCh
// fires. Each round uses exponential backoff (10 attempts, 100ms→5s). If a
// round is exhausted the cycle restarts, so the Writer never gives up.
func (w *Writer) reconnect() bool {
	w.reconnectMu.Lock()
	defer w.reconnectMu.Unlock()

	if w.getConn() != nil {
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-w.closeCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()

	dialer := net.Dialer{Timeout: writerDialTimeout}
	dialFn := func() (net.Conn, error) { return dialer.DialContext(ctx, "unix", w.socketPath) }

	for {
		newConn := utils.ReconnectWithBackoff(ctx, dialFn, utils.DefaultReconnectConfig())
		if newConn != nil {
			w.setConn(newConn)
			return true
		}
		select {
		case <-ctx.Done():
			return false
		default:
			glog.Warning("IPC writer: reconnect round exhausted, retrying")
		}
	}
}

func (w *Writer) write(msg Message) {
	for {
		select {
		case <-w.closeCh:
			return
		default:
		}

		conn := w.getConn()
		if conn == nil {
			if !w.reconnect() {
				return
			}
			conn = w.getConn()
		}

		if err := conn.SetWriteDeadline(time.Now().Add(writerWriteTimeout)); err != nil {
			glog.Warningf("IPC writer: failed to set write deadline: %v", err)
		}
		if err := Encode(conn, []Message{msg}); err != nil {
			glog.Errorf("IPC writer: write error: %v", err)
			w.setConn(nil)
			continue
		}
		return
	}
}
