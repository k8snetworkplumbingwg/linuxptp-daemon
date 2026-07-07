package ublox

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants to avoid goconst warnings
const (
	testProtoVersion  = "29.20"
	testProtoVersion2 = "29.25"
	testAntVoltCfg    = "CFG-HW-ANT_CFG_VOLTCTRL,1"
	testGPS           = "GPS"
)

type execCall struct {
	args []string
}

type execMock struct {
	calls         []execCall
	defaultOutput string
	defaultErr    error
	expectations  []execExpectation
}

type execExpectation struct {
	matchArgs []string
	output    string
	err       error
}

func (m *execMock) run(args ...string) ([]byte, error) {
	m.calls = append(m.calls, execCall{args: slices.Clone(args)})
	for _, exp := range m.expectations {
		needle := strings.Join(exp.matchArgs, "::")
		haystack := strings.Join(args, "::")
		if strings.Contains(haystack, needle) {
			return []byte(exp.output), exp.err
		}
	}
	return []byte(m.defaultOutput), m.defaultErr
}

func setupExecMock() (*execMock, func()) {
	orig := execCommand
	mock := &execMock{defaultOutput: "OK"}
	execCommand = mock.run
	return mock, func() { execCommand = orig }
}

func TestBuildArgs(t *testing.T) {
	t.Run("injects protocol version and wait", func(t *testing.T) {
		r := &CommandRunner{testProtoVersion}
		args := r.buildArgs([]string{"-z", testAntVoltCfg})
		assert.Equal(t, []string{"-P", testProtoVersion, "-w", DefaultWait, "-z", testAntVoltCfg}, args)
	})

	t.Run("skips -P when version is empty", func(t *testing.T) {
		r := &CommandRunner{}
		args := r.buildArgs([]string{"-z", testAntVoltCfg})
		assert.Equal(t, []string{"-w", DefaultWait, "-z", testAntVoltCfg}, args)
	})

	t.Run("skips -P when already in args", func(t *testing.T) {
		r := &CommandRunner{testProtoVersion}
		args := r.buildArgs([]string{"-P", testProtoVersion2, "-z", testAntVoltCfg})
		assert.Equal(t, []string{"-w", DefaultWait, "-P", testProtoVersion2, "-z", testAntVoltCfg}, args)
	})

	t.Run("skips -w when already in args", func(t *testing.T) {
		r := &CommandRunner{testProtoVersion}
		args := r.buildArgs([]string{"-w", "5", "-e", "SURVEYIN,600,50000"})
		assert.Equal(t, []string{"-P", testProtoVersion, "-w", "5", "-e", "SURVEYIN,600,50000"}, args)
	})

	t.Run("empty version and user-supplied -w", func(t *testing.T) {
		r := &CommandRunner{}
		args := r.buildArgs([]string{"-P", testProtoVersion2, "-w", "2", "arg"})
		assert.Equal(t, []string{"-P", testProtoVersion2, "-w", "2", "arg"}, args)
	})
}

func TestCommandRunnerRun(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	r := &CommandRunner{testProtoVersion}

	t.Run("success", func(t *testing.T) {
		result, err := r.Run(Command{Args: []string{"-e", testGPS}})
		require.NoError(t, err)
		assert.Equal(t, "OK", result)
		assert.Equal(t, 1, len(mock.calls))
		assert.Equal(t, []string{"-P", testProtoVersion, "-w", DefaultWait, "-e", testGPS}, mock.calls[0].args)
	})

	t.Run("error", func(t *testing.T) {
		mock.calls = nil
		mock.defaultErr = errors.New("failed")
		_, err := r.Run(Command{Args: []string{"-e", testGPS}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed")
		mock.defaultErr = nil
	})
}

func TestCommandRunnerRunAll(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()
	mock.expectations = []execExpectation{
		{matchArgs: []string{"-w", DefaultWait, "fail"}, output: "error output", err: errors.New("cmd failed")},
	}

	r := &CommandRunner{}

	cmds := CommandList{
		{Args: []string{"cmd1"}, ReportOutput: false},
		{Args: []string{"cmd2"}, ReportOutput: true},
		{Args: []string{"fail"}, ReportOutput: true},
		{Args: []string{"cmd4"}, ReportOutput: false},
	}

	results := r.RunAll(cmds, false)

	// cmd2 reports "OK", fail reports error
	require.Equal(t, 2, len(results))
	assert.Equal(t, "OK", results[0])
	assert.Contains(t, results[1], "cmd failed")
	assert.Equal(t, 4, len(mock.calls))
}

func TestCommandRunnerRunAllWithSave(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	r := &CommandRunner{testProtoVersion}

	cmds := CommandList{
		{Args: []string{"-e", testGPS}},
	}

	_ = r.RunAll(cmds, true)

	// Should have 2 calls: the command + SAVE
	require.Equal(t, 2, len(mock.calls))
	lastArgs := mock.calls[1].args
	assert.True(t, slices.Contains(lastArgs, "SAVE"), "last call should be SAVE, got: %v", lastArgs)
}

func TestCommandListRunAll(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()
	mock.expectations = []execExpectation{
		{matchArgs: cmdProtoVersion.Args, output: "PROTVER=29.20"},
	}

	cmds := CommandList{
		{Args: []string{"-P", testProtoVersion2, "-z", testAntVoltCfg}, ReportOutput: false},
		{Args: []string{"-P", testProtoVersion2, "-e", testGPS}, ReportOutput: true},
	}

	results := cmds.RunAll(true)

	// cmd2 reports output, plus SAVE at end
	require.Equal(t, 1, len(results))
	assert.Equal(t, "OK", results[0])

	// 3 calls: 2 commands + SAVE
	assert.Equal(t, 4, len(mock.calls))

	// No extra -P injection since commands already have -P
	for _, call := range mock.calls[1:3] {
		count := 0
		for _, arg := range call.args {
			if arg == "-P" {
				count++
			}
		}
		assert.Equal(t, 1, count, "should not double-inject -P: %v", call.args)
	}
}

func TestNewCommandRunner(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	mock.defaultOutput = "PROTVER=29.20"
	runner, err := NewCommandRunner()
	require.NoError(t, err)
	assert.Equal(t, testProtoVersion, runner.protoVersion)
	assert.Equal(t, 1, len(mock.calls))
}
