package pmc

import (
	"bufio"
	"context"
	"fmt"
	"net"
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

func helperSettings() protocol.GrandmasterSettings {
	return protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: 7, ClockAccuracy: fbprotocol.ClockAccuracyUnknown}, TimePropertiesDS: protocol.TimePropertiesDS{CurrentUtcOffset: 37, Leap61: true}}
}

func helperDatasets() ParentTimeCurrentDS {
	return ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterIdentity: "aabb.ccdd.eeff", GrandmasterClockClass: 6, ParentPortIdentity: "aabb.ccdd.eeff-1"}, TimePropertiesDS: helperSettings().TimePropertiesDS, CurrentDS: protocol.CurrentDS{StepsRemoved: 2}}
}

func TestPMCProcessHelper(_ *testing.T) {
	if os.Getenv("PMC_HELPER_PROCESS") != "1" {
		return
	}
	command := os.Args[len(os.Args)-1]
	mode := os.Getenv("PMC_HELPER_MODE")
	if selected := os.Getenv("PMC_HELPER_COMMAND"); selected != "" && command != selected {
		mode = "response"
	}
	switch mode {
	case "block":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		var socket string
		for index, arg := range os.Args {
			if arg == "-i" && index+1 < len(os.Args) {
				socket = os.Args[index+1]
			}
		}
		if err := os.WriteFile(socket, nil, 0600); err != nil {
			os.Exit(2)
		}
		connection, err := net.Dial("unix", os.Getenv("PMC_HELPER_READY"))
		if err != nil {
			os.Exit(2)
		}
		if _, err = fmt.Fprintf(connection, "%d %s\n", os.Getpid(), socket); err != nil {
			os.Exit(2)
		}
		_ = connection.Close()
		<-signals
	case "malformed":
		fmt.Print("invalid PMC response")
	case "partial":
		fmt.Print("clockClass 7\nclockAccuracy 0xfe\n")
	case "response":
		data := helperDatasets()
		switch {
		case strings.Contains(command, "GRANDMASTER_SETTINGS_NP"):
			settings := helperSettings()
			fmt.Print(settings.String())
		case strings.Contains(command, "EXTERNAL_GRANDMASTER_PROPERTIES_NP"):
			properties := protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "aabb.ccdd.eeff", StepsRemoved: 2}
			fmt.Print(properties.String())
		case command == cmdGetParentDataSet:
			fmt.Print("\n" + data.ParentDataSet.String())
		case command == cmdGetTimePropertiesDS:
			fmt.Print("\n currentUtcOffset 37\n leap61 1\n leap59 0\n currentUtcOffsetValid 0\n ptpTimescale 0\n timeTraceable 0\n frequencyTraceable 0\n timeSource 0x0\n")
		case command == cmdGetCurrentDS:
			fmt.Print(data.CurrentDS.String())
		}
	}
	os.Exit(0)
}

func configurePMCProcessHelper(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "pmc"), []byte("#!/bin/sh\nexec \"$PMC_HELPER_EXECUTABLE\" -test.run=TestPMCProcessHelper -- \"$@\"\n"), 0700))
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PMC_HELPER_EXECUTABLE", executable)
	t.Setenv("PMC_HELPER_PROCESS", "1")
	// Three instrumented children must fit the real request budget. Remove only
	// their race-runtime exit delay; the parent's detector remains unchanged.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	return directory
}

func contextOperations() map[string]func(context.Context) error {
	client := defaultClient{}
	return map[string]func(context.Context) error{
		"settings-get": func(ctx context.Context) error {
			_, err := client.GetGMSettingsContext(ctx, "ptp4l.test.config")
			return err
		},
		"settings-set": func(ctx context.Context) error {
			return client.SetGMSettingsContext(ctx, "ptp4l.test.config", helperSettings())
		},
		"datasets-get": func(ctx context.Context) error {
			_, err := client.GetParentTimeAndCurrentDSContext(ctx, "ptp4l.test.config")
			return err
		},
		"external-set": func(ctx context.Context) error {
			return client.SetExternalGMPropertiesNPContext(ctx, "ptp4l.test.config", protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "aabb.ccdd.eeff", StepsRemoved: 2})
		},
	}
}

func TestPMCContextCompleteResponsesAndAcknowledgments(t *testing.T) {
	configurePMCProcessHelper(t)
	t.Setenv("PMC_HELPER_MODE", "response")
	for name, operation := range contextOperations() {
		t.Run(name, func(t *testing.T) { require.NoError(t, operation(context.Background())) })
	}
	data, err := (defaultClient{}).GetParentTimeAndCurrentDSContext(context.Background(), "ptp4l.test.config")
	require.NoError(t, err)
	require.Equal(t, helperDatasets(), data)
	settings := helperSettings()
	settings.ClockQuality.ClockClass = 6
	require.ErrorContains(t, (defaultClient{}).SetGMSettingsContext(context.Background(), "ptp4l.test.config", settings), "acknowledged different")
	require.ErrorContains(t, (defaultClient{}).SetExternalGMPropertiesNPContext(context.Background(), "ptp4l.test.config", protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "aabb.ccdd.eeff", StepsRemoved: 3}), "acknowledged different")
	require.ErrorContains(t, (defaultClient{}).SetExternalGMPropertiesNPContext(context.Background(), "ptp4l.test.config", protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "0000.0000.0001", StepsRemoved: 2}), "acknowledged different")
}

func TestPMCContextRejectsEmptyMalformedAndPartialResponses(t *testing.T) {
	configurePMCProcessHelper(t)
	for _, mode := range []string{"empty", "malformed", "partial"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PMC_HELPER_MODE", mode)
			for name, operation := range contextOperations() {
				t.Run(name, func(t *testing.T) { require.Error(t, operation(context.Background())) })
			}
			for _, command := range []string{cmdGetTimePropertiesDS, cmdGetCurrentDS} {
				t.Run(command, func(t *testing.T) {
					t.Setenv("PMC_HELPER_COMMAND", command)
					_, err := (defaultClient{}).GetParentTimeAndCurrentDSContext(context.Background(), "ptp4l.test.config")
					require.Error(t, err)
				})
			}
		})
	}
}

func TestPMCContextCancellationReapsEachOwnedOperationAndSocket(t *testing.T) {
	directory := configurePMCProcessHelper(t)
	t.Setenv("PMC_HELPER_MODE", "block")
	operations := contextOperations()
	operations["datasets-time"] = operations["datasets-get"]
	operations["datasets-current"] = operations["datasets-get"]
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if name == "datasets-time" {
				t.Setenv("PMC_HELPER_COMMAND", cmdGetTimePropertiesDS)
			}
			if name == "datasets-current" {
				t.Setenv("PMC_HELPER_COMMAND", cmdGetCurrentDS)
			}
			address := filepath.Join(directory, "ready.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
			require.NoError(t, err)
			defer listener.Close()
			require.NoError(t, listener.SetDeadline(time.Now().Add(5*time.Second)))
			t.Setenv("PMC_HELPER_READY", address)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- operation(ctx) }()
			connection, err := listener.AcceptUnix()
			require.NoError(t, err)
			require.NoError(t, connection.SetReadDeadline(time.Now().Add(5*time.Second)))
			ready, err := bufio.NewReader(connection).ReadString('\n')
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			fields := strings.Fields(ready)
			require.Len(t, fields, 2)
			pid, err := strconv.Atoi(fields[0])
			require.NoError(t, err)
			cancel()
			select {
			case err = <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("canceled child did not join")
			}
			require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
			_, err = os.Stat(filepath.Dir(fields[1]))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
