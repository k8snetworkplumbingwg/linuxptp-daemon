package hardwareconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
)

func TestBatchPinSetContinuesAfterReadbackFailure(t *testing.T) {
	// Isolate glog in a subprocess: a failed first readback must be logged
	// without skipping later writes or their successful pin-state logs.
	if os.Getenv("PTP_BATCH_PIN_LOG_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBatchPinSetContinuesAfterReadbackFailure$", "-logtostderr=true")
		cmd.Env = append(os.Environ(), "PTP_BATCH_PIN_LOG_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		require.Contains(t, string(output), "failed to get pin 1: readback failed")
		require.Contains(t, string(output), "confirm pin=2")
		require.Contains(t, string(output), "pin-2")
		require.Contains(t, string(output), "connected")
		return
	}
	var operations []string
	send := func(command uint8, data []byte) error {
		require.Equal(t, uint8(dpll.DpllCmdPinSet), command)
		decoder, err := netlink.NewAttributeDecoder(data)
		require.NoError(t, err)
		require.True(t, decoder.Next())
		require.Equal(t, uint16(dpll.DpllPinID), decoder.Type())
		operations = append(operations, fmt.Sprintf("write %d", decoder.Uint32()))
		require.NoError(t, decoder.Err())
		return nil
	}
	read := func(req dpll.DoPinGetRequest) (*dpll.PinInfo, error) {
		operations = append(operations, fmt.Sprintf("read %d", req.ID))
		if req.ID == 1 {
			return nil, errors.New("readback failed")
		}
		return &dpll.PinInfo{ID: req.ID, BoardLabel: fmt.Sprintf("pin-%d", req.ID),
			ParentDevice: []dpll.PinParentDevice{{Direction: dpll.PinDirectionInput, State: dpll.PinStateConnected}}}, nil
	}
	commands := []dpll.PinParentDeviceCtl{{ID: 1}, {ID: 2}, {ID: 3}}
	require.NoError(t, batchPinSet(commands, send, read))
	require.Equal(t, []string{"write 1", "read 1", "write 2", "read 2", "write 3", "read 3"}, operations)
	glog.Flush()
}

func TestBatchPinSetReturnsSendError(t *testing.T) {
	// Unlike a diagnostic read failure, a failed write stops the batch.
	writes := 0
	reads := 0
	sendErr := errors.New("send failed")
	send := func(uint8, []byte) error {
		writes++
		if writes == 2 {
			return sendErr
		}
		return nil
	}
	read := func(req dpll.DoPinGetRequest) (*dpll.PinInfo, error) {
		reads++
		return &dpll.PinInfo{ID: req.ID}, nil
	}
	commands := []dpll.PinParentDeviceCtl{{ID: 1}, {ID: 2}, {ID: 3}}
	require.ErrorIs(t, batchPinSet(commands, send, read), sendErr)
	require.Equal(t, 2, writes)
	require.Equal(t, 1, reads)
}
