package ublox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
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
	// Extract ublox version information.
	regexProtoVersion = regexp.MustCompile(`PROTVER=(\d+\.\d+)`)

	// ackResponseCountOverrides contains commands whose ubxtool operation
	// produces more than one acknowledgement. Commands otherwise produce one
	// ACK per -e or -d argument. Combining multiple -d or -e operations is
	// potentially dangerous when any operation has an unknown response count,
	// because the ACK/NAK stream cannot then be attributed reliably.
	ackResponseCountOverrides = map[string]int{
		"GPS": 2, // nolint:goconst
	}

	// indeterminateAckCommands lists commands whose ubxtool operation produces
	// a variable-length ACK/NAK batch. The complete batch is reported together;
	// at least one ACK is considered success, while an all-NAK batch fails.
	indeterminateAckCommands = map[string]struct{}{
		"BINARY": {},
		"NMEA":   {},
	}

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

// defaultExecCommand runs ubxtool with a bounded execution timeout.
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

// CommandNAKError indicates that ubxtool ran successfully but the receiver
// rejected one or more requested operations. It is distinct from an error
// starting or communicating with ubxtool.
type CommandNAKError struct {
	Count int
}

// Error describes the number of negative acknowledgements received.
func (e *CommandNAKError) Error() string {
	return fmt.Sprintf("ubxtool command received %d ACK-NAK responses", e.Count)
}

// CommandList is a list of Commands.
type CommandList []Command

// Runner is the interface for executing ubxtool commands.
// CommandRunner is the production implementation; tests can substitute a mock.
type Runner interface {
	Run(cmd Command) (string, error)
	Poll(cmd Command, responseType MessageType) (Message, error)
	RunAll(cmds CommandList, withSave bool) []string
	Save() error
}

// defaultNewCommandRunnerFn constructs the production command runner.
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
	receiver     *UBlox
	commandMu    sync.Mutex
	issueFn      func(ctx context.Context, cmd Command) (<-chan error, error)
}

// NewCommandRunner detects the UBX protocol version and returns a runner.
func NewCommandRunner() (*CommandRunner, error) {
	runner := &CommandRunner{}
	version, err := runner.getVersion()
	if err != nil {
		return nil, fmt.Errorf("command runner init failed: %w", err)
	}
	runner.protoVersion = version
	glog.Infof("ubxtool: command runner initialized with protocol version %s", version)
	return runner, nil
}

// issueCommand starts a command asynchronously. Its output is intentionally
// not consumed here; callers receive responses through the receiver broker.
func (u *UBlox) issueCommand(ctx context.Context, cmd Command) (<-chan error, error) {
	runner := &CommandRunner{protoVersion: u.protoVersion}
	args := runner.buildArgs(cmd.Args)
	glog.Infof("ubxtool: running %s", strings.Join(args, " "))
	process := exec.CommandContext(ctx, UBXCommand, args...)
	if err := process.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() {
		done <- process.Wait()
		close(done)
	}()
	return done, nil
}

// Run executes a command. Poll commands continue to use the direct command
// path unless they are handled through Poll. Configuration commands use the
// receiver broker and wait for their ACK-ACK/ACK-NAK responses.
func (r *CommandRunner) Run(cmd Command) (string, error) {
	args := r.buildArgs(cmd.Args)
	if r.receiver == nil || !isAckCommand(cmd.Args) {
		return r.runDirect(args)
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	commands := ackCommandGroups(cmd.Args)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Subscribe to all messages so collectAckResponses can detect the first
	// non-ACK message that terminates an ACK/NAK batch.
	subscription := r.receiver.Subscribe(ctx)
	defer subscription.Cancel()

	issue := r.issueFn
	if issue == nil {
		issue = r.receiver.issueCommand
	}
	processDone, err := issue(ctx, cmd)
	if err != nil {
		return "", err
	}

	expectedResponses, determinate := ackBatchExpectation(commands)
	responses, collectErr := collectAckResponses(ctx, subscription.Messages, processDone, expectedResponses, determinate)
	if collectErr != nil {
		return "", collectErr
	}
	logAckCommandGroups(commands, responses)
	if batchErr := ackBatchError(commands, responses); batchErr != nil {
		return "", batchErr
	}
	if cmd.ReportOutput {
		var lines []string
		for _, response := range responses {
			lines = append(lines, response.Raw...)
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", nil
}

// runDirect executes a command whose output is read from its own process.
func (r *CommandRunner) runDirect(args []string) (string, error) {
	glog.Infof("ubxtool: running %s", strings.Join(args, " "))
	output, err := execCommand(args...)
	if err != nil {
		return string(output), fmt.Errorf("ubxtool %v failed: [%s] %w", args, string(output), err)
	}
	return string(output), nil
}

// Poll registers for the expected response type, issues a poll command, and
// waits for the parsed response from the long-running receiver.
func (r *CommandRunner) Poll(cmd Command, responseType MessageType) (Message, error) {
	if r.receiver == nil {
		return Message{}, errors.New("ublox poll requires a receiver")
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	subscription := r.receiver.Subscribe(ctx, responseType)
	defer subscription.Cancel()

	issue := r.issueFn
	if issue == nil {
		issue = r.receiver.issueCommand
	}
	processDone, err := issue(ctx, cmd)
	if err != nil {
		return Message{}, err
	}

	for {
		select {
		case message, ok := <-subscription.Messages:
			if !ok {
				return Message{}, fmt.Errorf("ublox poll subscription closed before receiving %s", responseType)
			}
			glog.Infof("ubxtool: command 1/1 (%s): %s", strings.Join(cmd.Args, " "), message.Type)
			return message, nil
		case processErr, ok := <-processDone:
			processDone = nil
			if ok && processErr != nil {
				return Message{}, fmt.Errorf("ublox poll command failed: %w", processErr)
			}
		case <-ctx.Done():
			return Message{}, fmt.Errorf("timed out waiting for ublox poll response %s", responseType)
		}
	}
}

// isPollCommand reports whether arguments request a UBX poll.
func isPollCommand(args []string) bool {
	return slices.Contains(args, "-p")
}

// pollResponseType extracts the response type requested by a poll command.
func pollResponseType(args []string) (MessageType, error) {
	for i, arg := range args {
		if arg != "-p" || i+1 >= len(args) {
			continue
		}
		name := args[i+1]
		if strings.HasPrefix(name, "UBX-") {
			return MessageType(name), nil
		}
		return MessageType("UBX-" + name), nil
	}
	return "", errors.New("poll command does not specify a response type")
}

// isAckCommand reports whether arguments contain an acknowledged operation.
func isAckCommand(args []string) bool {
	return len(ackCommandGroups(args)) > 0
}

// isSaveCommand reports whether arguments request persistent configuration.
func isSaveCommand(args []string) bool {
	for i, arg := range args {
		if arg == "-p" && i+1 < len(args) && args[i+1] == "SAVE" {
			return true
		}
	}
	return false
}

// ackCommandGroup describes the ACK/NAK responses expected from one operation.
type ackCommandGroup struct {
	description   string
	expected      int
	indeterminate bool
}

// commandAckCount returns the determinate ACK/NAK count for arguments.
func commandAckCount(args []string) int {
	count := 0
	for _, command := range ackCommandGroups(args) {
		count += command.expected
	}
	return count
}

// ackCommandDescriptions returns human-readable operation descriptions.
func ackCommandDescriptions(args []string) []string {
	commands := ackCommandGroups(args)
	descriptions := make([]string, 0, len(commands))
	for _, command := range commands {
		descriptions = append(descriptions, command.description)
	}
	return descriptions
}

// ackCommandGroups parses command arguments into response groups.
func ackCommandGroups(args []string) []ackCommandGroup {
	var commands []ackCommandGroup
	for i := 0; i < len(args); {
		switch args[i] {
		case "-z":
			// ubxtool batches a contiguous sequence of -z arguments into one
			// receiver command and therefore produces one ACK for the sequence.
			start := i
			for i < len(args) && args[i] == "-z" {
				i++
				if i < len(args) {
					i++
				}
			}
			commands = append(commands, ackCommandGroup{
				description: strings.Join(args[start:i], " "),
				expected:    1,
			})
		case "-e", "-d":
			start := i
			i++
			if i < len(args) {
				i++
			}
			responseCount := 1
			indeterminate := false
			if i-start == 2 {
				name := args[start+1]
				if _, found := indeterminateAckCommands[name]; found {
					indeterminate = true
					responseCount = 0
				} else if override, hasOverride := ackResponseCountOverrides[name]; hasOverride {
					responseCount = override
				}
			}
			commands = append(commands, ackCommandGroup{
				description:   strings.Join(args[start:i], " "),
				expected:      responseCount,
				indeterminate: indeterminate,
			})
		default:
			i++
		}
	}
	if len(commands) == 0 && isSaveCommand(args) {
		return []ackCommandGroup{{description: "-p SAVE", expected: 1}}
	}
	return commands
}

// ackBatchQuietPeriod leaves room within the two-second command context while
// allowing the slower command-response batches to arrive after the first ACK.
const ackBatchQuietPeriod = 500 * time.Millisecond

// ackBatchExpectation returns the total expected responses and whether all are known.
func ackBatchExpectation(commands []ackCommandGroup) (expected int, determinate bool) {
	determinate = true
	for _, command := range commands {
		expected += command.expected
		if command.indeterminate {
			determinate = false
		}
	}
	return expected, determinate
}

// collectAckResponses treats the first ACK/NAK and all subsequent ACK/NAK
// messages as one batch. Determinate batches end when their expected number
// of responses arrives; indeterminate batches end at the first non-ACK/NAK
// message or after ackBatchQuietPeriod without another response.
func collectAckResponses(ctx context.Context, messages <-chan Message, processDone <-chan error, expectedResponses int, determinate bool) ([]Message, error) {
	var responses []Message
	var processErr error
	var quietTimer *time.Timer
	var quietTimerC <-chan time.Time

	for {
		select {
		case message, ok := <-messages:
			if !ok {
				if len(responses) == 0 {
					return nil, errors.New("ublox ACK subscription closed before receiving a response")
				}
				return responses, nil
			}
			if message.Type != AckAckType && message.Type != AckNakType {
				if !determinate && len(responses) > 0 {
					return responses, nil
				}
				continue
			}
			responses = append(responses, message)
			if determinate && len(responses) >= expectedResponses {
				return responses, nil
			}
			if quietTimer == nil {
				quietTimer = time.NewTimer(ackBatchQuietPeriod)
				quietTimerC = quietTimer.C
			} else {
				if !quietTimer.Stop() {
					select {
					case <-quietTimer.C:
					default:
					}
				}
				quietTimer.Reset(ackBatchQuietPeriod)
			}
		case processErrValue, ok := <-processDone:
			processDone = nil
			if ok && processErrValue != nil {
				processErr = processErrValue
				if len(responses) == 0 {
					return nil, fmt.Errorf("ubxtool command failed: %w", processErr)
				}
			}
		case <-quietTimerC:
			if processErr != nil {
				return responses, fmt.Errorf("ubxtool command failed: %w", processErr)
			}
			return responses, nil
		case <-ctx.Done():
			if len(responses) > 0 {
				if processErr != nil {
					return responses, fmt.Errorf("ubxtool command failed: %w", processErr)
				}
				return responses, nil
			}
			return nil, fmt.Errorf("timed out waiting for ubxtool ACK responses: received %d", len(responses))
		}
	}
}

// countAckResponses counts positive and negative acknowledgements.
func countAckResponses(responses []Message) (ackCount, nakCount int) {
	for _, response := range responses {
		if response.Type == AckNakType {
			nakCount++
		} else {
			ackCount++
		}
	}
	return ackCount, nakCount
}

// logAckCommandGroups associates responses with operations in the command batch.
func logAckCommandGroups(commands []ackCommandGroup, responses []Message) {
	responseIndex := 0
	for commandIndex, command := range commands {
		description := shortAckCommandDescription(command.description)
		prefix := fmt.Sprintf("ubxtool: command %d/%d (%s):", commandIndex+1, len(commands), description)
		if command.indeterminate {
			commandResponses := responses[responseIndex:]
			ackCount, nakCount := countAckResponses(commandResponses)
			logAckCounts(prefix, ackCount, nakCount, len(commandResponses))
			responseIndex = len(responses)
			continue
		}

		end := responseIndex + command.expected
		if end > len(responses) {
			end = len(responses)
		}
		commandResponses := responses[responseIndex:end]
		ackCount, nakCount := countAckResponses(commandResponses)
		if command.expected == 1 && len(commandResponses) == 1 {
			if nakCount > 0 {
				glog.Infof("%s UBX-ACK-NAK", prefix)
			} else {
				glog.Infof("%s UBX-ACK-ACK", prefix)
			}
		} else {
			logAckCounts(prefix, ackCount, nakCount, command.expected)
		}
		responseIndex = end
	}
	if responseIndex < len(responses) {
		ackCount, nakCount := countAckResponses(responses[responseIndex:])
		glog.Infof("ubxtool: received unexpected ACK batch: %d ACK-ACK, %d ACK-NAK", ackCount, nakCount)
	}
}

// logAckCounts records aggregate acknowledgement counts for an operation.
func logAckCounts(prefix string, ackCount, nakCount, total int) {
	statuses := []string{fmt.Sprintf("%d/%d UBX-ACK-ACK", ackCount, total)}
	if nakCount > 0 {
		statuses = append(statuses, fmt.Sprintf("%d/%d UBX-ACK-NAK", nakCount, total))
	}
	glog.Infof("%s %s", prefix, strings.Join(statuses, ", "))
}

// ackBatchError converts missing or negative responses into command errors.
func ackBatchError(commands []ackCommandGroup, responses []Message) error {
	responseIndex := 0
	for _, command := range commands {
		if command.indeterminate {
			ackCount, nakCount := countAckResponses(responses[responseIndex:])
			if ackCount == 0 && nakCount > 0 {
				return &CommandNAKError{Count: nakCount}
			}
			return nil
		}

		end := responseIndex + command.expected
		if end > len(responses) {
			return fmt.Errorf("received %d of %d expected ACK/NAK responses for %s", len(responses)-responseIndex, command.expected, command.description)
		}
		_, nakCount := countAckResponses(responses[responseIndex:end])
		if nakCount > 0 {
			return &CommandNAKError{Count: nakCount}
		}
		responseIndex = end
	}
	return nil
}

// shortAckCommandDescription bounds long batch descriptions for logging.
func shortAckCommandDescription(description string) string {
	const maxDescriptionLength = 40
	if !strings.HasPrefix(description, "-z ") || len(description) <= maxDescriptionLength {
		return description
	}
	return description[:maxDescriptionLength-3] + "..."
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

// getVersion directly queries MON-VER to discover the UBX protocol version.
// This is intentionally kept separate from the broker-backed command APIs:
// version discovery happens before the long-running receiver is available.
func (r *CommandRunner) getVersion() (string, error) {
	args := r.buildArgs([]string{"-w", QueryTimeout, "-p", "MON-VER"})
	output, err := r.runDirect(args)
	if err != nil {
		return "", err
	}
	match := regexProtoVersion.FindStringSubmatch(output)
	if len(match) > 0 {
		return match[1], nil
	}
	return "", fmt.Errorf("ubxtool output did not match %s: %s", regexProtoVersion.String(), output)
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
