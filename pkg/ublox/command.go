package ublox

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/golang/glog"
)

const (
	// DefaultWait is the default ubxtool wait time (seconds) used when -w is not specified.
	DefaultWait = "0.1"

	// QueryTimeout is a longer wait time (seconds) used for commands that expect output
	// (e.g., MON-VER, MON-HW).
	QueryTimeout = "0.5"

	// execTimeout is the maximum time to wait for a single ubxtool invocation
	// before killing the process. Prevents hung ubxtool from blocking forever.
	execTimeout = 30 * time.Second
)

var (
	// Query ublox version information
	cmdProtoVersion = Command{Args: []string{"-p", "MON-VER"}}
	// Extract ublox version information
	regexProtoVersion = regexp.MustCompile(`PROTVER=(\d+\.\d+)`)

	// execCommand is the low-level function used to execute ubxtool.
	// Replace in tests to mock ubxtool execution.
	execCommand = defaultExecCommand

	// NewCommandRunnerFn creates a Runner. Replace in tests to mock
	// command execution at the runner level instead of the exec level.
	NewCommandRunnerFn = defaultNewCommandRunnerFn

	// SaveCommand is the ubxtool command that persists the current configuration.
	SaveCommand = Command{
		Args: []string{"-p", "SAVE"},
	}
)

func defaultExecCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	return exec.CommandContext(ctx, UBXCommand, args...).CombinedOutput()
}

// Command represents a single ubxtool command.
type Command struct {
	ReportOutput bool     `json:"reportOutput"`
	Args         []string `json:"args"`
}

// CommandList is a list of Commands.
type CommandList []Command

// Runner is the interface for executing ubxtool commands.
// CommandRunner is the production implementation; tests can substitute a mock.
type Runner interface {
	Run(cmd Command) (string, error)
	RunAll(cmds CommandList, withSave bool) []string
	Save() error
}

func defaultNewCommandRunnerFn() (Runner, error) {
	return NewCommandRunner()
}

// RunAll creates a convenience CommandRunner wrapper and executes all commands
// with protocol detection and '-P' injection as needed. If withSave is true, a
// SAVE command is appended. Returns output from commands that have
// ReportOutput set.
func (cmds CommandList) RunAll(withSave bool) []string {
	runner, err := NewCommandRunnerFn()
	if err != nil {
		glog.Warningf("ubxtool error: %v", err)
		return []string{
			err.Error(),
		}
	}
	return runner.RunAll(cmds, withSave)
}

// CommandRunner executes ubxtool commands with automatic protocol version
// and wait time handling.
type CommandRunner struct {
	protoVersion string
}

// NewCommandRunner detects the UBX protocol version and returns a runner.
func NewCommandRunner() (*CommandRunner, error) {
	runner := &CommandRunner{}
	version, err := runner.Query(cmdProtoVersion, regexProtoVersion)
	if err != nil {
		return nil, fmt.Errorf("command runner init failed: %w", err)
	}
	runner.protoVersion = version
	glog.Infof("ubxtool: command runner initialized with protocol version %s", version)
	return runner, nil
}

// Run executes a single command. It prepends -P <version> if the runner has a
// protocol version and the command doesn't already include -P. It also prepends
// -w <DefaultWait> if -w is not already in the args.
func (r *CommandRunner) Run(cmd Command) (string, error) {
	args := r.buildArgs(cmd.Args)
	glog.Infof("ubxtool: running %s", strings.Join(args, " "))
	output, err := execCommand(args...)
	if err != nil {
		return string(output), fmt.Errorf("ubxtool %v failed: [%s] %w", args, string(output), err)
	}
	return string(output), nil
}

// RunAll executes a list of commands. If withSave is true, a SAVE command is
// appended. Returns output from commands that have ReportOutput set.
func (r *CommandRunner) RunAll(cmds CommandList, withSave bool) []string {
	var results []string
	for _, cmd := range cmds {
		result, err := r.Run(cmd)
		if err != nil {
			glog.Warningf("ubxtool error: %v", err)
			if cmd.ReportOutput {
				results = append(results, err.Error())
			}
			continue
		}
		if cmd.ReportOutput {
			glog.Infof("ubxtool: recording output: %s", result)
			results = append(results, result)
		}
	}
	if withSave {
		if err := r.Save(); err != nil {
			glog.Warningf("ubxtool SAVE error: %v", err)
		}
	}
	return results
}

// Save persists the current ublox configuration.
func (r *CommandRunner) Save() error {
	_, err := r.Run(SaveCommand)
	return err
}

// Query executes a command with QueryTimeout and matches the output against
// the given regex. Returns the first capture group on success.
func (r *CommandRunner) Query(cmd Command, promptRE *regexp.Regexp) (string, error) {
	// Use QueryTimeout for commands expecting output
	cmd.Args = append([]string{"-w", QueryTimeout}, cmd.Args...)
	output, err := r.Run(cmd)
	if err != nil {
		return "", err
	}
	match := promptRE.FindStringSubmatch(output)
	if len(match) > 0 {
		return match[1], nil
	}
	return "", fmt.Errorf("ubxtool output did not match %s: %s", promptRE.String(), output)
}

// buildArgs constructs the full argument list for a ubxtool invocation.
func (r *CommandRunner) buildArgs(args []string) []string {
	var fullArgs []string
	if r.protoVersion != "" && !slices.Contains(args, "-P") {
		fullArgs = append(fullArgs, "-P", r.protoVersion)
	}
	if !slices.Contains(args, "-w") {
		fullArgs = append(fullArgs, "-w", DefaultWait)
	}
	fullArgs = append(fullArgs, args...)
	return fullArgs
}
