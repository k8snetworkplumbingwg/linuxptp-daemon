package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// startTestSocket creates a Unix socket listener at a temp path and accepts
// connections in the background. Returns the socket path and a cleanup function.
func startTestSocket(t *testing.T) (string, func()) {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create test socket: %v", err)
	}
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					if _, readErr := c.Read(buf); readErr != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return sockPath, func() {
		ln.Close()
		os.Remove(sockPath)
	}
}

// NewTestPMCProcess creates a PMCProcess with injectable dependencies for testing.
func NewTestPMCProcess(
	t *testing.T,
	configFileName, clockType string,
	getMonitorFn func(string) (*expect.GExpect, <-chan error, error),
	pollFn func(string, bool) (protocol.ParentDataSet, error),
) *PMCProcess {
	sockPath, cleanup := startTestSocket(t)
	t.Cleanup(cleanup)
	return &PMCProcess{
		configFileName:       configFileName,
		messageTag:           "[" + configFileName + ":{level}]",
		monitorParentData:    true,
		parentDSCh:           make(chan protocol.ParentDataSet, 10),
		clockType:            clockType,
		outSocketPath:        sockPath,
		exitCh:               make(chan struct{}, 1),
		getMonitorFn:         getMonitorFn,
		runPMCExpGetParentDS: pollFn,
	}
}
