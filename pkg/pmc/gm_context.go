package pmc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// GMSettingsClient owns cancellable GM settings requests. A call must not return
// until its transport has stopped; cancellation must interrupt blocked I/O.
type GMSettingsClient interface {
	GetGMSettingsContext(context.Context, string) (protocol.GrandmasterSettings, error)
	SetGMSettingsContext(context.Context, string, protocol.GrandmasterSettings) error
}

func (defaultClient) GetGMSettingsContext(ctx context.Context, cfg string) (protocol.GrandmasterSettings, error) {
	return runGMSettings(ctx, cfg, cmdGetGMSettings)
}

func (defaultClient) SetGMSettingsContext(ctx context.Context, cfg string, settings protocol.GrandmasterSettings) error {
	actual, err := runGMSettings(ctx, cfg, cmdSetGMSettings+strings.ReplaceAll(settings.String(), "\n", " "))
	if err == nil && actual != settings {
		return fmt.Errorf("PMC acknowledged different GM settings")
	}
	return err
}

func runGMSettings(ctx context.Context, cfg, command string) (settings protocol.GrandmasterSettings, err error) {
	localDir, err := os.MkdirTemp("", "pmc-gm-")
	if err != nil {
		return settings, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(localDir)) }()
	if err = os.Chmod(localDir, 0711); err != nil {
		return settings, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pmc", "-u", "-b", "0", "-f", "/var/run/"+cfg, "-i", filepath.Join(localDir, "socket"), command)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return protocol.GrandmasterSettings{}, ctxErr
	}
	if err != nil {
		return protocol.GrandmasterSettings{}, fmt.Errorf("PMC GM settings: %w", err)
	}
	return parseGMSettings(string(output))
}

func parseGMSettings(output string) (protocol.GrandmasterSettings, error) {
	var settings protocol.GrandmasterSettings
	matches := grandmasterSettingsNPRegExp.FindStringSubmatch(output)
	if len(matches) != len(settings.Keys())+1 {
		return settings, fmt.Errorf("PMC response has no complete GRANDMASTER_SETTINGS_NP")
	}
	for index, value := range matches[1:] {
		settings.Update(settings.Keys()[index], value)
	}
	return settings, nil
}
