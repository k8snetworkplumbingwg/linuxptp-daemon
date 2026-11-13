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

// DownstreamClient owns all transport used to forward an upstream announce.
type DownstreamClient interface {
	GMSettingsClient
	GetParentTimeAndCurrentDSContext(context.Context, string) (ParentTimeCurrentDS, error)
	SetExternalGMPropertiesNPContext(context.Context, string, protocol.ExternalGrandmasterProperties) error
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
	output, err := runContext(ctx, cfg, command)
	if err != nil {
		return settings, err
	}
	return parseGMSettings(output)
}

func runContext(ctx context.Context, cfg, command string) (result string, err error) {
	localDir, err := os.MkdirTemp("", "pmc-gm-")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(localDir)) }()
	if err = os.Chmod(localDir, 0711); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pmc", "-u", "-b", "0", "-f", "/var/run/"+cfg, "-i", filepath.Join(localDir, "socket"), command)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", fmt.Errorf("PMC command: %w", err)
	}
	return string(output), nil
}

func (defaultClient) GetParentTimeAndCurrentDSContext(ctx context.Context, cfg string) (result ParentTimeCurrentDS, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	parent, err := runContext(ctx, cfg, cmdGetParentDataSet)
	if err != nil {
		return result, err
	}
	parsedParent, err := protocol.ProcessMessage[protocol.ParentDataSet](parentDataSetRegExp.FindStringSubmatch(parent))
	if err != nil {
		return result, err
	}
	timeOutput, err := runContext(ctx, cfg, cmdGetTimePropertiesDS)
	if err != nil {
		return result, err
	}
	parsedTime, err := protocol.ProcessMessage[protocol.TimePropertiesDS](timePropertiesDSRegExp.FindStringSubmatch(timeOutput))
	if err != nil {
		return result, err
	}
	current, err := runContext(ctx, cfg, cmdGetCurrentDS)
	if err != nil {
		return result, err
	}
	parsedCurrent, err := protocol.ProcessMessage[protocol.CurrentDS](currentDSRegExp.FindStringSubmatch(current))
	if err != nil {
		return result, err
	}
	return ParentTimeCurrentDS{ParentDataSet: *parsedParent, TimePropertiesDS: *parsedTime, CurrentDS: *parsedCurrent}, nil
}

func (defaultClient) SetExternalGMPropertiesNPContext(ctx context.Context, cfg string, properties protocol.ExternalGrandmasterProperties) error {
	output, err := runContext(ctx, cfg, cmdSetExternalGMPropertiesNP+strings.ReplaceAll(properties.String(), "\n", " "))
	if err != nil {
		return err
	}
	actual, err := protocol.ProcessMessage[protocol.ExternalGrandmasterProperties](externalGMPropertiesNPRegExp.FindStringSubmatch(output))
	if err != nil {
		return err
	}
	if *actual != properties {
		return fmt.Errorf("PMC acknowledged different external GM properties")
	}
	return nil
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
