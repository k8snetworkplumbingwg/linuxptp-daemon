package pmc

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

const (
	// Command strings for PMC operations
	cmdGetParentDataSet          = "GET PARENT_DATA_SET"
	cmdGetGMSettings             = "GET GRANDMASTER_SETTINGS_NP"
	cmdSetGMSettings             = "SET GRANDMASTER_SETTINGS_NP"
	cmdGetExternalGMPropertiesNP = "GET EXTERNAL_GRANDMASTER_PROPERTIES_NP"
	cmdSetExternalGMPropertiesNP = "SET EXTERNAL_GRANDMASTER_PROPERTIES_NP"
	cmdGetTimePropertiesDS       = "GET TIME_PROPERTIES_DATA_SET"
	cmdGetCurrentDS              = "GET CURRENT_DATA_SET"

	// Timeout and retry constants
	cmdTimeout     = 2000 * time.Millisecond
	sigTimeout     = 500 * time.Millisecond
	maxRetries     = 6
	pollTimeout    = 3 * time.Second
	restartDelay   = 250 * time.Millisecond
	maxBackoff     = 5 * time.Second
	initialBackoff = 250 * time.Millisecond

	// PMC command prefix
	pmcCmdPrefix = "pmc -u -b 0 -f /var/run/"

	// PMC subscription commands
	cmdGetSubscribeEventsNP = "GET SUBSCRIBE_EVENTS_NP"
	cmdSetSubscribeEventsNP = "SET SUBSCRIBE_EVENTS_NP duration 0 NOTIFY_PORT_STATE off NOTIFY_TIME_SYNC off NOTIFY_PARENT_DATA_SET on"
)

var (
	grandmasterSettingsNPRegExp  = regexp.MustCompile((&protocol.GrandmasterSettings{}).RegEx())
	parentDataSetRegExp          = regexp.MustCompile((&protocol.ParentDataSet{}).RegEx())
	externalGMPropertiesNPRegExp = regexp.MustCompile((&protocol.ExternalGrandmasterProperties{}).RegEx())
	timePropertiesDSRegExp       = regexp.MustCompile((&protocol.TimePropertiesDS{}).RegEx())
	currentDSRegExp              = regexp.MustCompile((&protocol.CurrentDS{}).RegEx())
)

// RunPMCExp ... go expect to run PMC util cmd
func RunPMCExp(configFileName, cmdStr string, promptRE *regexp.Regexp) (result string, matches []string, err error) {
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return "", []string{}, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, matches, err = e.Expect(promptRE, cmdTimeout)
		if err != nil {
			glog.Errorf("pmc result match error %s", err)
			return
		}
		glog.Infof("pmc result: %s", result)
	}
	return
}

// RunPMCExpGetGMSettings ... get current GRANDMASTER_SETTINGS_NP
func RunPMCExpGetGMSettings(configFileName string) (g protocol.GrandmasterSettings, err error) {
	cmdStr := cmdGetGMSettings
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return g, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for i := 0; i < maxRetries; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			result, matches, err1 := e.Expect(grandmasterSettingsNPRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return g, err1
			}
			glog.Infof("pmc result: %s", result)
			for i, m := range matches[1:] {
				g.Update(g.Keys()[i], m)
			}
			break
		}
	}
	return
}

// RunPMCExpSetGMSettings ... set GRANDMASTER_SETTINGS_NP
func RunPMCExpSetGMSettings(configFileName string, g protocol.GrandmasterSettings) (err error) {
	cmdStr := cmdSetGMSettings
	cmdStr += strings.Replace(g.String(), "\n", " ", -1)
	pmcCmd := pmcCmdPrefix + configFileName
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, _, err1 := e.Expect(grandmasterSettingsNPRegExp, cmdTimeout)
		if err1 != nil {
			glog.Errorf("pmc result match error %v", err1)
			return err1
		}
		glog.Infof("pmc result: %s", result)
	}
	return
}

// RunPMCExpGetParentDS ... GET PARENT_DATA_SET
func RunPMCExpGetParentDS(configFileName string) (p protocol.ParentDataSet, err error) {
	cmdStr := cmdGetParentDataSet
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return p, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for i := 0; i < maxRetries; i++ {
		glog.Infof("%s retry %d", cmdGetParentDataSet, i)
		if err = e.Send(cmdStr + "\n"); err == nil {
			result, matches, err1 := e.Expect(parentDataSetRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return p, err1
			}
			glog.Infof("pmc result: %s", result)
			for i, m := range matches[1:] {
				p.Update(p.Keys()[i], m)
			}
			break
		}
	}
	return
}

// RunPMCExpGetExternalGMPropertiesNP ... get current EXTERNAL_GRANDMASTER_PROPERTIES_NP
func RunPMCExpGetExternalGMPropertiesNP(configFileName string) (egp protocol.ExternalGrandmasterProperties, err error) {
	cmdStr := cmdGetExternalGMPropertiesNP
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for i := 0; i < maxRetries; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			result, matches, err1 := e.Expect(externalGMPropertiesNPRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return egp, err1
			}
			glog.Infof("pmc result: %s", result)
			for j, m := range matches[1:] {
				egp.Update(egp.Keys()[j], m)
			}
			break
		}
	}
	return
}

// RunPMCExpSetExternalGMPropertiesNP ... set EXTERNAL_GRANDMASTER_PROPERTIES_NP
func RunPMCExpSetExternalGMPropertiesNP(configFileName string, egp protocol.ExternalGrandmasterProperties) (err error) {
	cmdStr := cmdSetExternalGMPropertiesNP
	cmdStr += strings.Replace(egp.String(), "\n", " ", -1)
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("Sending %s %s", pmcCmd, cmdStr)

	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, dbg, err1 := e.Expect(externalGMPropertiesNPRegExp, cmdTimeout)
		if err1 != nil {
			glog.Errorf("pmc result match error %v", err1)
			glog.Errorf("pmc result: %s", dbg)
			return err1
		}
		glog.Infof("pmc result: %s", result)
	}
	return
}

// RunPMCExpGetTimePropertiesDS ... "GET TIME_PROPERTIES_DATA_SET"
func RunPMCExpGetTimePropertiesDS(configFileName string) (tp protocol.TimePropertiesDS, err error) {
	cmdStr := cmdGetTimePropertiesDS
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for i := 0; i < maxRetries; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			_, matches, err1 := e.Expect(timePropertiesDSRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return tp, err1
			}
			for j, m := range matches[1:] {
				tp.Update(tp.Keys()[j], m)
			}
			glog.Infof("pmc result: %++v", tp)
			break
		}
	}
	return
}

// RunPMCExpGetCurrentDS ... "GET CURRENT_DATA_SET"
func RunPMCExpGetCurrentDS(configFileName string) (cds protocol.CurrentDS, err error) {
	cmdStr := cmdGetCurrentDS
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for i := 0; i < maxRetries; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			_, matches, err1 := e.Expect(currentDSRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return cds, err1
			}
			for j, m := range matches[1:] {
				cds.Update(cds.Keys()[j], m)
			}
			glog.Infof("pmc result: %++v", cds)
			break
		}
	}
	return
}

// RunPMCGetParentDS runs PMC in non-interactive mode to get PARENT_DATA_SET
// This function does not use the expect package and handles regex parsing separately
func RunPMCGetParentDS(configFileName string) (p protocol.ParentDataSet, err error) {
	cmdStr := cmdGetParentDataSet
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)

	// Run PMC command in non-interactive mode
	cmd := exec.Command("pmc", "-u", "-b", "0", "-f", "/var/run/"+configFileName, cmdStr)

	output, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		glog.Errorf("pmc command execution error: %v", cmdErr)
		return p, cmdErr
	}

	// Convert output to string and apply regex parsing
	result := string(output)
	glog.Infof("pmc result: %s", result)

	// Apply regex parsing separately
	matches := parentDataSetRegExp.FindStringSubmatch(result)
	if len(matches) < 2 {
		glog.Errorf("pmc result regex match failed, no matches found")
		return p, fmt.Errorf("failed to parse PMC output")
	}

	// Parse the matched groups (skip first match which is the full string)
	for j, m := range matches[1:] {
		if j < len(p.Keys()) {
			p.Update(p.Keys()[j], m)
		}
	}

	return p, nil
}

// MultipleResults holds the results from multiple PMC commands
type MultipleResults struct {
	ParentDataSet    protocol.ParentDataSet
	TimePropertiesDS protocol.TimePropertiesDS
	CurrentDS        protocol.CurrentDS
}

// RunPMCExpGetMultiple runs PMC in interactive mode and sends three commands sequentially:
// GET PARENT_DATA_SET, GET TIME_PROPERTIES_DATA_SET, and GET CURRENT_DATA_SET
// This is more efficient than spawning separate PMC processes for each command
func RunPMCExpGetMultiple(configFileName string) (MultipleResults, error) {
	var results MultipleResults
	
	pmcCmd := pmcCmdPrefix + configFileName
	glog.Infof("Running multiple PMC commands for %s", configFileName)

	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return results, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	// Command 1: GET PARENT_DATA_SET
	glog.Infof("Sending command: %s", cmdGetParentDataSet)
	if err = e.Send(cmdGetParentDataSet + "\n"); err != nil {
		return results, fmt.Errorf("failed to send PARENT_DATA_SET command: %w", err)
	}
	
	result, matches, err := e.Expect(parentDataSetRegExp, cmdTimeout)
	if err != nil {
		return results, fmt.Errorf("failed to get PARENT_DATA_SET: %w", err)
	}
	glog.Infof("PARENT_DATA_SET result: %s", result)
	
	// Parse ParentDataSet
	keys := results.ParentDataSet.Keys()
	for i, match := range matches[1:] {
		if i < len(keys) {
			results.ParentDataSet.Update(keys[i], match)
		}
	}

	// Command 2: GET TIME_PROPERTIES_DATA_SET
	glog.Infof("Sending command: %s", cmdGetTimePropertiesDS)
	if err = e.Send(cmdGetTimePropertiesDS + "\n"); err != nil {
		return results, fmt.Errorf("failed to send TIME_PROPERTIES_DATA_SET command: %w", err)
	}
	
	result, matches, err = e.Expect(timePropertiesDSRegExp, cmdTimeout)
	if err != nil {
		return results, fmt.Errorf("failed to get TIME_PROPERTIES_DATA_SET: %w", err)
	}
	glog.Infof("TIME_PROPERTIES_DATA_SET result: %s", result)
	
	// Parse TimePropertiesDS
	keys = results.TimePropertiesDS.Keys()
	for i, match := range matches[1:] {
		if i < len(keys) {
			results.TimePropertiesDS.Update(keys[i], match)
		}
	}

	// Command 3: GET CURRENT_DATA_SET
	glog.Infof("Sending command: %s", cmdGetCurrentDS)
	if err = e.Send(cmdGetCurrentDS + "\n"); err != nil {
		return results, fmt.Errorf("failed to send CURRENT_DATA_SET command: %w", err)
	}
	
	result, matches, err = e.Expect(currentDSRegExp, cmdTimeout)
	if err != nil {
		return results, fmt.Errorf("failed to get CURRENT_DATA_SET: %w", err)
	}
	glog.Infof("CURRENT_DATA_SET result: %s", result)
	
	// Parse CurrentDS
	keys = results.CurrentDS.Keys()
	for i, match := range matches[1:] {
		if i < len(keys) {
			results.CurrentDS.Update(keys[i], match)
		}
	}

	return results, nil
}

// SubscribeToClockClassEvents starts a PMC process to subscribe to clock class (PARENT_DATA_SET) notifications.
// It listens for NOTIFY_PARENT_DATA_SET events and invokes the provided callback with the parsed ClockClass value
// whenever a notification is received. The lifecycle of this goroutine should be managed by the daemon
// (start/stop with ptp4l).
//
// The callback receives the new clock class (as uint8) and the full ParentDataSet struct.
// The clock class is extracted from the "gm.ClockClass" field of the ParentDataSet notification.
//
// Example usage:
//
//	stopCh := make(chan struct{})
//	go func() {
//	    err := pmc.SubscribeToClockClassEvents("ptp4l.conf", func(clockClass uint8, parentDS *protocol.ParentDataSet) {
//	        glog.Infof("Clock class changed: %d", clockClass)
//	        // You can update state, notify other components, etc.
//	        // parentDS contains all fields from the notification, e.g.:
//	        // parentDS.ParentPortIdentity, parentDS.GrandmasterIdentity, parentDS.GrandmasterClockClass, etc.
//	    }, stopCh)
//	    if err != nil {
//	        glog.Errorf("SubscribeToClockClassEvents exited: %v", err)
//	    }
//	}()
//
//	// ... later, to stop listening (e.g., when ptp4l stops):
//	close(stopCh)
//
// The *protocol.ParentDataSet passed to the callback is fully populated with all fields
// from the PARENT_DATA_SET notification, such as:
//   - ParentPortIdentity
//   - ParentStats
//   - ObservedParentOffsetScaledLogVariance
//   - ObservedParentClockPhaseChangeRate
//   - GrandmasterPriority1
//   - GrandmasterClockClass
//   - GrandmasterClockAccuracy
//   - GrandmasterOffsetScaledLogVariance
//   - GrandmasterPriority2
//   - GrandmasterIdentity
//
// and any other fields defined in the protocol.ParentDataSet struct.
//
// The subscription duration is set to 0, which per linuxptp pmc.c means "subscribe indefinitely" (see pmc.c: SUBSCRIBE_EVENTS_NP).
// If the pmc process or ptp4l process exits, this function will automatically attempt to restart the subscription.
// SubscribeToClockClassEvents starts a persistent PMC subscription and invokes
// onClockClassChange every time a PARENT_DATA_SET notification is received.
// It restarts PMC on exit/EOF and stops cleanly when stopCh is closed.
func SubscribeToClockClassEvents(
	configFileName string,
	onClockClassChange func(clockClass uint8, parentDS *protocol.ParentDataSet),
	stopCh <-chan struct{},
) error {
	backoff := initialBackoff
	const maxRetries = 10 // Increase max retries for better resilience

	for {
		select {
		case <-stopCh:
			glog.Infof("Stopping PMC clock class event subscription for %s", configFileName)
			return nil
		default:
		}

		// Wait longer for ptp4l to be fully ready before starting PMC
		// This helps avoid race conditions where ptp4l gets recreated
		time.Sleep(5 * time.Second)

		pmcCmd := pmcCmdPrefix + configFileName
		glog.Infof("Starting PMC for clock class event subscription: %s", pmcCmd)

		e, r, err := expect.Spawn(pmcCmd, -1)
		if err != nil {
			glog.Errorf("Failed to spawn PMC: %v", err)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		// Reset backoff after a successful spawn
		backoff = initialBackoff

		// Watcher to terminate PMC on stop/cleanup
		cleanup := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-stopCh:
				if err := e.SendSignal(syscall.SIGTERM); err != nil {
					glog.Warningf("Failed to send SIGTERM to PMC: %v", err)
				}
			case <-cleanup:
				return
			}
		}()

		// Check if ptp4l is ready by trying to get current subscription status
		// This helps ensure ptp4l is fully initialized before proceeding
		if err := e.Send("GET SUBSCRIBE_EVENTS_NP\n"); err != nil {
			glog.Errorf("Failed to send GET SUBSCRIBE_EVENTS_NP: %v", err)
			_ = e.Close()
			close(cleanup)
			<-done
			time.Sleep(backoff)
			continue
		}

		// Wait for response with a shorter timeout for readiness check
		if _, _, err := e.Expect(regexp.MustCompile("SUBSCRIBE_EVENTS_NP"), 10*time.Second); err != nil {
			glog.Warningf("ptp4l not ready yet, retrying in %v: %v", backoff, err)
			_ = e.Close()
			close(cleanup)
			<-done
			time.Sleep(backoff)
			continue
		}

		// ptp4l is ready, proceed with subscription setup
		glog.Infof("ptp4l is ready, setting up subscription for %s", configFileName)

		// Set subscription for PARENT_DATA_SET notifications
		setCmd := "SET SUBSCRIBE_EVENTS_NP duration 0 NOTIFY_PORT_STATE off NOTIFY_TIME_SYNC off NOTIFY_PARENT_DATA_SET on\n"
		if err := e.Send(setCmd); err != nil {
			glog.Errorf("Failed to send SET SUBSCRIBE_EVENTS_NP: %v", err)
			_ = e.Close()
			close(cleanup)
			<-done
			time.Sleep(backoff)
			continue
		}

		if _, _, err := e.Expect(regexp.MustCompile("SUBSCRIBE_EVENTS_NP"), cmdTimeout); err != nil {
			glog.Errorf("Failed to get SET SUBSCRIBE_EVENTS_NP response: %v", err)
			_ = e.Close()
			close(cleanup)
			<-done
			time.Sleep(backoff)
			continue
		}

		glog.Infof("Successfully set up PMC subscription for %s", configFileName)

		// Notification loop
	notificationLoop:
		for {
			select {
			case <-stopCh:
				if err := e.SendSignal(syscall.SIGTERM); err != nil {
					glog.Warningf("Failed to send SIGTERM to PMC: %v", err)
				}
				_ = e.Close()
				close(cleanup)
				<-done
				return nil

			case <-r:
				// PMC exited; break to restart
				glog.Warningf("PMC process for clock class events exited, restarting...")
				break notificationLoop

			default:
				// Wait for notification with timeout
				_, matches, err := e.Expect(parentDataSetRegExp, pollTimeout)
				if err != nil {
					if _, ok := err.(expect.TimeoutError); ok {
						// poll again
						continue
					}
					// Check if the error indicates the process has ended
					if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "exit") {
						glog.Warningf("PMC process exited (%v), restarting...", err)
						break notificationLoop
					}
					glog.Errorf("Error waiting for PARENT_DATA_SET notification: %v", err)
					continue
				}

				// Process the notification
				parentDS := &protocol.ParentDataSet{}
				keys := parentDS.Keys()
				
				if len(matches)-1 < len(keys) {
					glog.Warningf("Short match (%d vs %d) for PARENT_DATA_SET", len(matches)-1, len(keys))
				}
				
				for i, m := range matches[1:] {
					if i < len(keys) {
						parentDS.Update(keys[i], m)
					}
				}

				clockClass := parentDS.GrandmasterClockClass
				if onClockClassChange != nil {
					func() {
						defer func() {
							if rcv := recover(); rcv != nil {
								glog.Errorf("onClockClassChange panic: %v", rcv)
							}
						}()
						onClockClassChange(clockClass, parentDS)
					}()
				}
			}
		}

		// Cleanup before restart
		_ = e.Close()
		close(cleanup)
		<-done

		// Small delay between restarts
		time.Sleep(restartDelay)
	}
}
