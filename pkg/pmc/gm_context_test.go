package pmc

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/require"
)

func TestPMCProcessHelper(_ *testing.T) {
	if os.Getenv("PMC_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("PMC_HELPER_MODE") {
	case "block":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		for index, arg := range os.Args {
			if arg == "-i" && index+1 < len(os.Args) {
				localSocket := os.Args[index+1]
				if err := os.WriteFile(localSocket, nil, 0600); err != nil {
					os.Exit(2)
				}
				if err := os.WriteFile(os.Getenv("PMC_HELPER_SOCKET_REPORT"), []byte(localSocket), 0600); err != nil {
					os.Exit(2)
				}
			}
		}
		if err := os.WriteFile(os.Getenv("PMC_HELPER_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		<-signals
	case "response":
		settings := protocol.GrandmasterSettings{}
		settings.ClockQuality.ClockClass = fbprotocol.ClockClass7
		settings.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
		fmt.Print(settings.String())
	}
	os.Exit(0)
}

func TestGMContextTransportRejectsNoResponseAndReapsCancellation(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pmc"), []byte("#!/bin/sh\nexec \"$PMC_HELPER_EXECUTABLE\" -test.run=TestPMCProcessHelper -- \"$@\"\n"), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PMC_HELPER_EXECUTABLE", executable)
	t.Setenv("PMC_HELPER_PROCESS", "1")
	t.Setenv("PMC_HELPER_MODE", "empty")
	_, err = (defaultClient{}).GetGMSettingsContext(context.Background(), "ptp4l.test.config")
	require.ErrorContains(t, err, "no complete GRANDMASTER_SETTINGS_NP", "exit zero without response is not GET success")
	t.Setenv("PMC_HELPER_MODE", "response")
	settings, err := (defaultClient{}).GetGMSettingsContext(context.Background(), "ptp4l.test.config")
	require.NoError(t, err)
	require.Equal(t, fbprotocol.ClockClass7, settings.ClockQuality.ClockClass)
	require.NoError(t, (defaultClient{}).SetGMSettingsContext(context.Background(), "ptp4l.test.config", settings))
	settings.ClockQuality.ClockClass = fbprotocol.ClockClass6
	require.ErrorContains(t, (defaultClient{}).SetGMSettingsContext(context.Background(), "ptp4l.test.config", settings), "acknowledged different")
	t.Setenv("PMC_HELPER_MODE", "block")
	pidFile := filepath.Join(dir, "child.pid")
	t.Setenv("PMC_HELPER_PID", pidFile)
	socketReport := filepath.Join(dir, "child.socket")
	t.Setenv("PMC_HELPER_SOCKET_REPORT", socketReport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, callErr := (defaultClient{}).GetGMSettingsContext(ctx, "ptp4l.test.config")
		done <- callErr
	}()
	var pidBytes []byte
	require.Eventually(t, func() bool { pidBytes, err = os.ReadFile(pidFile); return err == nil && len(pidBytes) != 0 }, time.Second, time.Millisecond)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	cancel()
	select {
	case callErr := <-done:
		require.Error(t, callErr)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled transport did not join subprocess")
	}
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "transport must reap the child before returning")
	socketPath, err := os.ReadFile(socketReport)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Dir(string(socketPath)))
	require.ErrorIs(t, err, os.ErrNotExist, "owned UDS directory must be removed after the child is reaped")
}
