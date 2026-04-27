# Current Behavior Analysis: Process Failure Handling

**Purpose**: Document the current code-level behavior for reference during
planning and implementation. This supplements `spec.md` (which describes
desired behavior in domain terms) with implementation details.

**Feature**: [spec.md](./spec.md)
**Date**: 2026-04-18

## Process Restart Mechanism

Each managed process (ptp4l, phc2sys, ts2phc, synce4l) runs in a `cmdRun`
loop in `pkg/daemon/daemon.go`. When a process exits:

1. stdout EOF is detected → `cmd.Wait()` returns
2. `processStatus(..., PtpProcessDown)` emits a down status
3. For ts2phc: `updateGMStatusOnProcessDown` sends a `Reset` event on
   `eventCh`, which clears TS2PHC-related event state, metrics, and
   `clkSyncState` in the event handler
4. For T-BC ptp4l: `AfterRunPTPCommand(&p.nodeProfile, "reset-to-default")`
   runs before the restart sleep
5. After a 1-second delay (`connectionRetryInterval`), the process is
   recreated and restarted — unless `Stopped()` is true

There is no exponential backoff for main process restarts — only a fixed
1-second gap.

## gpspipe Named Pipe Failure

`gpspipe.CmdRun` in `pkg/daemon/gpspipe.go` calls `mkFifo()` at the top of
each restart loop iteration. If `mkFifo()` fails after 5 retries with
exponential backoff (100ms → 1.6s total):

- **Production**: `glog.Fatalf` terminates the entire daemon process. All
  PTP processes crash. Kubernetes restarts the pod.
- **Test environment** (`SKIP_GNSS_MONITORING=1`): returns an error, and
  `CmdRun` returns (exits the goroutine). No supervisor restarts the
  goroutine — only a full profile reconciliation (`applyNodePTPProfiles`)
  would restart it.

`gpspipe.MonitorProcess` is an unimplemented stub (`//TODO implement me`).

Triggers: external deletion of `/gpsd/data`, filesystem permission changes,
disk-full conditions, mount namespace issues.

## Dependency Process Lifecycle

gpsd and gpspipe are "dependency processes" of ts2phc, started from
`applyNodePtpProfile` via `go d.CmdRun(false)`. When the parent
`ptpProcess` (ts2phc) dies and its `cmdRun` recreates only `cmd`, it does
**not** restart dependency goroutines. Dependencies only restart if they
also exit independently or a full `applyNodePTPProfiles` stop/start cycle
runs.

## T-BC State Machine (`pkg/event/event_tbc.go`)

The BC FSM in `updateBCState`:

- FREERUN/NOTSET → LOCKED: when `inSyncCondition` and not source lost
- LOCKED → FREERUN: on `freeRunCondition` (large offset vs thresholds)
- LOCKED → HOLDOVER: on `isSourceLostBC` (no ptp4l port LOCKED or DPLL
  not locked)
- HOLDOVER → LOCKED: when in sync and source not lost
- HOLDOVER → FREERUN: on `freeRunCondition`

`isSourceLostBC` currently checks PTP port state and DPLL lock status.
It does **not** treat TR ptp4l process death as a source-lost condition —
this is the gap that FR-006 addresses.

## T-GM State Machine (`pkg/event/event.go`)

`updateGMState` combines GNSS, DPLL, and ts2phc states. The `Reset` event
sent on ts2phc death clears:
- `e.data[cfgName]` (all event data for the config)
- `e.clockClass` → `ClockClassUninitialized`
- `e.clockAccuracy` → `ClockAccuracyUnknown`
- `e.outOfSpec` and `e.frequencyTraceable`

This full state wipe is problematic when ts2phc is expected to restart
quickly — DPLL state and GNSS fix status are still valid but get cleared.
This is the issue FR-012 addresses.

## DPLL Holdover (`pkg/dpll/dpll.go`)

`stateDecision()` evaluates DPLL hardware state. When DPLL reports
`DPLL_HOLDOVER`:
- GNSS source: enters holdover if `inSpec`, starts `holdover()` goroutine
- PTP source: checks offset vs `LocalMaxHoldoverOffSet`

`holdover()` runs a 1Hz ticker, synthesizes phase offset from
slope × elapsed time, and compares against `MaxInSpecOffset` and
`LocalHoldoverTimeout`. Timeout → FREERUN.

## Event Emission on Process Death

| Process | On Death | On Restart |
|---------|----------|------------|
| ptp4l (any) | `PTP_PROCESS_STATUS:0` | `PTP_PROCESS_STATUS:1` |
| ts2phc | `PTP_PROCESS_STATUS:0` + `Reset` event clearing GM state | `PTP_PROCESS_STATUS:1` |
| phc2sys | `PTP_PROCESS_STATUS:0` + FREERUN state events | `PTP_PROCESS_STATUS:1` + LOCKED after sync |
| gpsd | `PTP_PROCESS_STATUS:0` + GNSS `Reset` on monitor context done | `PTP_PROCESS_STATUS:1` |
| gpspipe | `PTP_PROCESS_STATUS:0` | `PTP_PROCESS_STATUS:1` |
| DPLL | `Reset` event for that interface on netlink monitor stop | Reconnects via netlink redial (250ms) |

## PMC Clock Class Announcements

- **T-BC**: `handleParentDS` calls `UpdateUpstreamParentDataSet` to feed
  the BC FSM. `downstreamAnnounceIWF` pushes upstream GM settings to
  controlled (TT) ports via PMC.
- **T-GM**: `UpdateClockClass` uses PMC to set `GRANDMASTER_SETTINGS_NP`
  and emits `CLOCK_CLASS_CHANGE` log line.
- **OC**: `handleParentDS` calls `AnnounceClockClass` on
  `GrandmasterClockClass` change.
- 60-second `classTicker` re-sends `CLOCK_CLASS_CHANGE` for persistence.

## Key Code Locations

| Area | File |
|------|------|
| Process lifecycle, T-BC transitions | `pkg/daemon/daemon.go` |
| gpspipe, mkFifo | `pkg/daemon/gpspipe.go` |
| gpsd, GNSS monitor | `pkg/daemon/gpsd.go` |
| PMC, clock class from Parent DS | `pkg/daemon/pmc.go` |
| Log parsing, offset thresholds | `pkg/daemon/log_parsing.go` |
| GM state machine, event loop | `pkg/event/event.go` |
| BC FSM, downstream PMC, holdover | `pkg/event/event_tbc.go` |
| Per-interface aggregation | `pkg/event/stats.go` |
| DPLL holdover goroutine | `pkg/dpll/dpll.go` |
| ptp4l parser, port roles | `pkg/parser/ptp4l_parser.go` |
| Clock class log format | `pkg/utils/clock_class_log.go` |
| HardwareConfig resolution | `pkg/hardwareconfig/hardwareconfig.go` |
