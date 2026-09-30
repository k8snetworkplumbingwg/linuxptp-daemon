package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindProcessesByName(t *testing.T) {
	pm := &ProcessManager{
		process: asProcesses(
			&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName}},
			&ptpProcess{ExecProcess: ExecProcess{name: phc2sysProcessName}},
			&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName}},
		),
	}

	procs := pm.findProcessesByName(ptp4lProcessName)
	assert.Equal(t, 2, len(procs))
	assert.Equal(t, ptp4lProcessName, procs[0].Name())
	assert.Equal(t, ptp4lProcessName, procs[1].Name())

	procs = pm.findProcessesByName(phc2sysProcessName)
	assert.Equal(t, 1, len(procs))

	procs = pm.findProcessesByName("nonexistent")
	assert.Equal(t, 0, len(procs))
}
