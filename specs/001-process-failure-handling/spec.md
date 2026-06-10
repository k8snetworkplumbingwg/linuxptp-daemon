# Feature Specification: Process Failure Handling & Clock Class Announcement

**Feature Branch**: `001-process-failure-handling`
**Created**: 2026-04-18
**Status**: Draft
**Input**: User description: "Analyze and fix daemon process failure handling across clock modes (T-BC, T-GM, OC, BC) and correct clock class change announcements when underlying daemons restart"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - T-BC or T-TSC TR ptp4l Failure Enters Holdover (Priority: P1)

When the time-receiver (TR) ptp4l process in a T-BC / T-TSC deployment
crashes and restarts, the system MUST treat the outage as a source-lost
condition. If the system was in LOCKED state, it MUST enter HOLDOVER
immediately — identical to how an upstream link-down is handled today. If
the system was in any other state (FREERUN, NOTSET), it MUST remain in or
transition to FREERUN. Upon ptp4l recovery and PTP adjacency
re-establishment, the system follows the normal path: determining sufficient
synchronization, locking DPLL on the PHC, and entering LOCKED state.
Currently, the daemon emits `os-clock-sync-state-change` and
`synchronization-state-change` events for `CLOCK_REALTIME` (the same events
phc2sys emits), which is incorrect: the outage affects the upstream PTP
source, not the system clock synchronization.

**Why this priority**: TR ptp4l failure in T-BC or T-TSC is the most operationally
impactful scenario. Incorrect event emission misleads monitoring systems and
operators about the nature of the outage. Holdover enables downstream
clients to maintain timing accuracy during the recovery window specified by the holdover parameters.

**Independent Test**: Kill the TR ptp4l process in a T-BC / T-TSC deployment, verify
the system transitions to HOLDOVER (not FREERUN), emits clock class change
events (class 135 for in-spec holdover), and returns to LOCKED after ptp4l
recovers and ports return to SLAVE state. Verify no spurious
`CLOCK_REALTIME` events are emitted during the outage.

**Acceptance Scenarios**:

1. **Given** a T-BC or T-TSC deployment with TR ptp4l running and clock
   state LOCKED,
   **When** the TR ptp4l process is killed,
   **Then** the BC state machine transitions to HOLDOVER immediately,
   clock class changes to 135 (in-spec holdover), and a clock class change
   event is emitted.

2. **Given** a T-BC or T-TSC deployment with TR ptp4l running and clock
   state FREERUN or NOTSET,
   **When** the TR ptp4l process is killed,
   **Then** the BC state machine remains in (or transitions to) FREERUN
   with clock class 248.

3. **Given** a T-BC or T-TSC deployment in HOLDOVER due to TR ptp4l
   restart,
   **When** the TR ptp4l process recovers, PTP adjacency is
   re-established, sufficient synchronization is determined, and DPLL
   locks on the PHC,
   **Then** the BC state machine transitions to LOCKED, clock class reverts
   to the upstream GM class, and a clock class change event is emitted.

4. **Given** a T-BC or T-TSC deployment in HOLDOVER due to TR ptp4l kill,
   **When** the holdover duration exceeds the DPLL's in-spec holdover
   timeout,
   **Then** the clock class changes to 165 (out-of-spec holdover) and a
   corresponding clock class change event is emitted.

5. **Given** a T-BC or T-TSC deployment with TR ptp4l killed,
   **When** the process restarts within the acceptable downtime threshold,
   **Then** no `os-clock-sync-state-change` or `synchronization-state-change`
   events for `CLOCK_REALTIME` are emitted during the outage window.

---

### User Story 2 - T-BC TT ptp4l Failure Toggles Freerun (Priority: P1)

When the time-transmitter (TT) ptp4l process in a T-BC deployment crashes
and restarts, the daemon MUST emit a FREERUN state-change log line and
update metrics (for cloud-event-proxy and monitoring) and return to LOCKED
once the TT ptp4l recovers and its ports return to MASTER state. Downstream
PTP clients will independently detect the loss via ANNOUNCE_RECEIPT_TIMEOUT;
no pre-emptive PMC push to the dead TT ptp4l is required. Currently, no
events are emitted at all, leaving monitoring consumers unaware of the
outage.

**Why this priority**: TT ptp4l failure directly affects all downstream PTP
clients. Silent failures violate the observability principle and can cause
downstream clock drift without operator awareness.

**Independent Test**: Kill the TT ptp4l process in a T-BC deployment, verify
a FREERUN event is emitted, and verify the system returns to LOCKED after
recovery. Verify clock class reflects the FREERUN state during the outage.

**Acceptance Scenarios**:

1. **Given** a T-BC deployment with TT ptp4l running and ports in MASTER
   state,
   **When** the TT ptp4l process is killed,
   **Then** a state change event to FREERUN is emitted and clock class
   changes to the freerun class (248).

2. **Given** a T-BC deployment with TT ptp4l restarting,
   **When** the TT ptp4l process recovers and its ports return to MASTER
   state,
   **Then** a state change event to LOCKED is emitted and clock class
   reverts to the upstream GM class.

---

### User Story 3 - Configurable Process Downtime Threshold (Priority: P1)

The system MUST support a configurable "maximum acceptable process downtime"
per process type. When a process restarts within this threshold, the daemon
MUST suppress transient state-change events that would otherwise cause
unnecessary alarm. When the threshold is exceeded, the daemon MUST emit
the appropriate state transition events. A threshold of 0 (the default)
means every restart triggers events immediately.


**Why this priority**: This is the foundation that enables event suppression
for short-lived process restarts (phc2sys, ts2phc) while still detecting
genuine failures. The ptp-operator already has the CR field merged
(ptp-operator PR #174).

**Independent Test**: Configure a 5-second downtime threshold for phc2sys on T-BC / T-TSC.
Kill phc2sys, verify no state-change events are emitted during the first
5 seconds, and verify events are emitted if phc2sys fails to restart within
5 seconds.

**Acceptance Scenarios**:

1. **Given** a downtime threshold of 5 seconds configured for phc2sys,
   **When** phc2sys is killed and restarts within 3 seconds,
   **Then** no `os-clock-sync-state-change` or `synchronization-state-change`
   events are emitted during the outage.

2. **Given** a downtime threshold of 5 seconds configured for phc2sys,
   **When** phc2sys is killed and fails to restart within 5 seconds,
   **Then** the appropriate state-change events are emitted after the
   threshold expires.

3. **Given** a downtime threshold of 0 (default) for any process,
   **When** the process is killed,
   **Then** state-change events are emitted immediately (current behavior
   preserved).

4. **Given** a per-process downtime threshold configuration,
   **When** different thresholds are set for ptp4l (0s), phc2sys (5s), and
   ts2phc (3s),
   **Then** each process respects its own threshold independently.

---

### User Story 4 - Short-Lived phc2sys/ts2phc Restarts Are Silenced (Priority: P2)

When phc2sys or ts2phc crashes and restarts within ~1 second with no
measurable time error impact, the system MUST suppress the transient
LOCKED→FREERUN→LOCKED event sequence. Currently, phc2sys restart causes
a full event toggle (`os-clock-sync-state-change` and
`synchronization-state-change` LOCKED→FREERUN→LOCKED) and ts2phc restart
may fail to recover entirely (CNF-21499).

**Why this priority**: These spurious events generate false alerts in
monitoring systems and erode operator confidence in the event pipeline.
Fixing ts2phc recovery (CNF-21499) is a prerequisite for reliable silencing.

**Independent Test**: Kill phc2sys in a running T-BC or T-GM deployment,
verify it restarts within the configured threshold, and verify no
state-change events are emitted. Repeat for ts2phc and verify recovery
works (CNF-21499 fixed).

**Acceptance Scenarios**:

1. **Given** a T-BC deployment with phc2sys running and the downtime
   threshold set above phc2sys typical restart time (~1s),
   **When** phc2sys is killed and restarts within the threshold,
   **Then** no LOCKED→FREERUN→LOCKED event toggle is emitted.

2. **Given** a T-GM deployment with ts2phc running,
   **When** ts2phc is killed,
   **Then** ts2phc restarts successfully (CNF-21499 resolved), and if the
   restart is within the threshold, no transient GM status events are
   emitted.

3. **Given** a T-GM deployment with ts2phc killed,
   **When** ts2phc fails to restart within the threshold,
   **Then** a GM status change event to FREERUN is emitted. The system
   cannot maintain holdover with ts2phc down.

---

### User Story 5 - T-GM Process Failure Behavior (Priority: P2)

When processes fail in a T-GM deployment, the system MUST handle each
process type appropriately:
- **ts2phc failure** (**holdover-breaking** while down past the threshold):
  Per FR-009 / FR-013, do not downgrade state or class until the downtime
  threshold expires; then FREERUN and class 248. If recovered within the
  threshold, suppress the suppressed event categories (FR-017).
- **ptp4l failure**: Since T-GM ptp4l is a time transmitter, downstream
  clients lose their master. The system MUST announce freerun clock class
  until ptp4l recovers.
- **gpsd/gpspipe failure** (**holdover-capable** when ts2phc keeps running
  and the GM can remain in true holdover): Short outages SHOULD be
  tolerated via the downtime threshold (no premature class or state
  changes). Extended outages MUST trigger GNSS-lost state transitions and
  announcements consistent with actual timing state (FR-013).

**Why this priority**: T-GM is the timing root; incorrect behavior here
propagates errors to all downstream devices in the network.

**Independent Test**: In a T-GM deployment, kill each process type
individually and verify the correct state transitions and clock class
announcements per the acceptance scenarios.

**Acceptance Scenarios**:

1. **Given** a T-GM deployment in LOCKED state with a downtime threshold configured,
   **When** ts2phc is killed and recovered within the downtime threshold,
   **Then** no events are emitted.
2. **Given** a T-GM deployment in LOCKED state with a downtime threshold configured,
   **When** ts2phc is killed and not recovered within the downtime threshold,
   **Then** the system enters FREERUN state and emits the appropriate state-change events.
3. **Given** a T-GM deployment in FREERUN state with a downtime threshold configured,
   **When** ts2phc is killed and recovered within the downtime threshold,
   **Then** no events are emitted.
4. **Given** a T-GM deployment in FREERUN state with a downtime threshold configured,
   **When** ts2phc is killed and not recovered within the downtime threshold,
   **Then** the system remains in FREERUN state and emits the appropriate state-change events.
5. **Given** a T-GM deployment in LOCKED state with a downtime threshold configured,
   **When** ptp4l is killed and not recovered within the downtime threshold,
   **Then** the system announces freerun clock class (248) to indicate
   downstream clients have lost their master.
6. **Given** a T-GM deployment in LOCKED state with a downtime threshold configured,
   **When** gpsd is killed and recovered within the downtime threshold,
   **Then** no events are emitted.
7. **Given** a T-GM deployment in LOCKED state with a downtime threshold configured,
   **When** gpspipe is killed and recovered within the downtime threshold,
   **Then** no events are emitted.


---


### Edge Cases

- What happens when multiple processes fail simultaneously (e.g., ptp4l and
  phc2sys both killed)?
  The system MUST handle each failure individually and emit the appropriate state-change events.
- How does the system behave when a process enters a crash loop (repeated
  failures within seconds)?
  The downtime threshold timer starts on the first crash and does not reset
  on subsequent crashes. The process must be up for 2 consecutive seconds
  (stabilization period) within the original threshold window to be
  considered recovered. For example, with a 5-second threshold: if a process
  crashes at T=0, restarts at T=1, crashes again at T=1.5, restarts at T=2,
  it must remain stable from T=2 to T=4 (2s stabilization) — still within
  the 5s window. If the process is still crash-looping at T=5, events are
  emitted. This prevents crash loops from indefinitely suppressing events.
- What happens when TR ptp4l is killed during an ongoing holdover from a
  different cause (e.g., upstream link down)?
  If the system is in holdover and T-BC / T-TSC TR ptp4l is killed, it doesn't change the holdover state.
- How does the downtime threshold interact with the DPLL holdover timeout?
  The downtime threshold is independent of the DPLL holdover timeout.
- What happens when ts2phc dependency processes (gpsd, gpspipe, DPLL) fail
  but ts2phc itself remains running?
  The system MUST handle each failure individually and emit the appropriate state-change events.
- What happens when a process is killed or fails to start during profile
  reconfiguration?
  If the system is not in LOCKED when reconfiguration begins, managed
  processes MUST be restarted using the same rules as today. **Duplicate**
  timing announcements for the same logical condition MUST NOT occur: see
  **FR-018** (and **SC-009**).
- How does the system handle gpspipe when the named pipe cannot be created?
  Currently, pipe creation failure either terminates the entire daemon
  (production) or leaves gpspipe permanently dead until a full profile
  reconfiguration (no automatic recovery). Triggers include: external
  deletion of the named pipe, filesystem permission changes, disk-full
  conditions, or mount namespace issues.
  No change to the current behavior is needed. See
  [current-behavior.md](./current-behavior.md) for implementation details.

## Clarifications

### Session 2026-04-18

- Q: Does the downtime threshold timer reset on each new crash or continue from the first crash? → A: Timer continues from the first crash. The 2-second stabilization period must complete within the original threshold window. This prevents crash loops from indefinitely suppressing events.
- Q: Are synce4l and chronyd failures in scope? → A: Out of scope. They operate independently (SyncE is physical-layer frequency, chronyd is optional NTP bridge) and their failures don't interact with the PTP clock state machine.
- Q: Is OC and BC (non-telecom) failure handling in scope? → A: Deferred. FR-014 is retained as a noted requirement but implementation is deferred to a follow-up feature. T-BC/T-GM modes are the critical path.
- Q: What GM state should the system enter when ts2phc fails past the downtime threshold — FREERUN or HOLDOVER (following DPLL)? → A: Always FREERUN. The system cannot maintain holdover with ts2phc down; the timing chain from GNSS/1PPS to PHC is broken regardless of DPLL hardware state.
- Q: How do FR-009 and FR-013 relate for T-GM (clock class 7 vs FREERUN)? → A: Class 7 applies only when the GM is genuinely in holdover (outages that do not break the ts2phc-mediated timing path). ts2phc loss past the threshold is holdover-breaking: state FREERUN and announced class 248, not 7.
- Q: May clock state or class change before the downtime threshold expires for threshold-governed failures? → A: No. Until the threshold expires (per FR-004), clock state, PMC clock class, and suppressed synchronization/GM-status events must not change solely because of that outage. Immediate transitions required elsewhere (e.g. FR-006) are unchanged.
- Q: How does TT ptp4l failure get communicated to downstream clients? → A: Daemon-side only. The daemon emits log lines and metric updates for the cloud-event-proxy and monitoring systems. Downstream PTP clients detect the loss via ANNOUNCE_RECEIPT_TIMEOUT. No pre-emptive PMC push to a dead TT ptp4l is needed.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The daemon MUST detect process death ASAP (current behavior preserved).

- **FR-002**: The daemon MUST automatically restart any crashed process
  ASAP unless the process was explicitly stopped (current behavior preserved).

- **FR-003**: The daemon MUST support a configurable "maximum acceptable
  process downtime" threshold per process type, sourced from PtpConfig
  CR settings. Default MUST be 0 (immediate event emission).

- **FR-004**: When a process restarts and maintains up status for 2
  consecutive seconds (stabilization) within its configured downtime
  threshold window (measured from the first crash), the daemon MUST
  suppress transient state-change events that would otherwise be emitted
  during the outage. The timer does not reset on subsequent crashes.

- **FR-005**: When a process fails to restart within its configured
  downtime threshold, the daemon MUST emit the appropriate state-change events.

- **FR-006**: In T-BC / T-TSC mode, TR ptp4l process death MUST trigger
  HOLDOVER if the system was in LOCKED state, or FREERUN if the system
  was in any other state (FREERUN, NOTSET). Recovery follows the normal
  path: PTP adjacency re-establishment, sufficient synchronization
  determination, DPLL lock on PHC, then transition to LOCKED.

- **FR-008**: In T-BC mode, TR ptp4l process death MUST NOT emit
  `os-clock-sync-state-change` or `synchronization-state-change` events
  for `CLOCK_REALTIME`. These events are only appropriate for phc2sys
  failures affecting system clock synchronization.

- **FR-009**: In T-GM mode, ts2phc process death is **holdover-breaking**:
  the timing chain from GNSS/1PPS through ts2phc to the PHC is not
  credible while ts2phc is down, regardless of DPLL hardware indications.
  **Until** the configured downtime threshold expires without stable
  recovery (FR-004), the daemon MUST NOT transition GM timing state to
  FREERUN, MUST NOT announce freerun clock class for this cause, and MUST
  inhibit the clock-state, clock-class, and synchronization/GM-status
  events that the threshold suppresses (FR-017). **After** the threshold
  expires without recovery, GM timing state MUST transition to FREERUN
  and announcements MUST match that state (FR-013 holdover-breaking case;
  class 248). This is mandatory: downstream clients must not observe a
  holdover class for a broken GNSS-to-PHC path once the outage is treated
  as real.

- **FR-010**: In T-GM mode, ptp4l (time transmitter) process death MUST
  announce freerun clock class to downstream clients.

- **FR-011**: The ts2phc process MUST restart and become functional within
  3 seconds of process death, 100% of the time (addresses CNF-21499).

- **FR-012**: The `Reset` event sent on ts2phc death MUST NOT clear
  state that is still valid (e.g., DPLL state, GNSS fix status) while
  the process is within its configured downtime threshold window.

- **FR-013**: Clock class announcements via PMC MUST reflect the **actual**
  timing state, not merely that a helper process restarted. **T-BC /
  T-TSC**: In holdover, the announced class MUST be 135 (in-spec) or 165
  (out-of-spec); in freerun, the announced class MUST be 248. **T-GM**:
  distinguish **holdover-capable** outages from **holdover-breaking**
  outages:
  - **Holdover-capable** (the authoritative timing path through ts2phc to
    the PHC remains valid; e.g. transient **gpsd** or **gpspipe** failure
    while ts2phc is still running and the GM remains in a true holdover
    timing state): the announced class MUST be 7 when the GM is in
    holdover; when locked, announcements MUST match the normal locked GM
    quality for the deployment (same as today when no outage applies).
  - **Holdover-breaking** (the path is not credible for holdover; e.g.
    **ts2phc** dead beyond the downtime threshold per FR-009, or T-GM
    **ptp4l** (time transmitter) failure per FR-010): the GM is in FREERUN for timing purposes
    and the announced class MUST be **248**, not 7. Announcing class 7
    while claiming holdover when ts2phc has failed past the threshold is
    inconsistent with FR-009 and MUST NOT occur.

- **FR-014**: [DEFERRED] In OC and BC modes (non-telecom), ptp4l failure
  MUST emit correct state-change events even when DPLL is not present.
  Deferred to a follow-up feature; T-BC/T-GM modes are the critical path.

- **FR-015**: The gpspipe process MUST auto-restart after named pipe
  creation failure without requiring a full profile reconciliation cycle.

- **FR-016**: The daemon MUST emit `PTP_PROCESS_STATUS` (down/up) for
  every process death and restart regardless of the downtime threshold.
  The threshold only suppresses clock-state and synchronization events.

- **FR-017**: For any managed process with a configured downtime
  threshold greater than zero, until that threshold expires without
  stable recovery (FR-004), the daemon MUST NOT, **based solely on that
  process outage**, change clock state, change the clock class announced
  via PMC, or emit the clock-state / synchronization / GM-status events
  that FR-003–FR-005 suppress. **Exception**: requirements that mandate an
  **immediate** transition on process death (notably FR-006 for TR ptp4l
  in T-BC / T-TSC) are not delayed by a downtime threshold. FR-016 is
  unchanged: process down/up visibility is always emitted.

- **FR-018**: During **profile reconfiguration** (coordinated restart of
  managed processes), when clock timing state and the corresponding
  announcements for that sequence have **already** been emitted (for
  example FREERUN while processes restart), the daemon MUST NOT emit a
  **second** equivalent clock-state transition, clock-class announcement,
  or duplicate of the same category of synchronization / GM-status events
  solely because a managed process later fails to start or dies within
  that reconfiguration window. Operators and monitoring consumers MUST see
  at most one logical transition per distinct timing condition. FR-016
  remains: `PTP_PROCESS_STATUS` (down/up) for the failing process MAY
  still be emitted where applicable; this requirement targets **duplicate
  timing-state and class signaling**, not process-level status. If the
  system is not in LOCKED when reconfiguration begins, process restart
  behavior MUST match today’s logic; FR-018 adds the non-duplication
  obligation on top.

### Key Entities

- **Process**: A managed child process (ptp4l, phc2sys, ts2phc, synce4l,
  gpsd, gpspipe) with lifecycle state, restart count, and downtime timer.
- **Downtime Threshold**: Per-process-type configurable duration (seconds)
  below which transient state events are suppressed.
- **Clock State**: The aggregate timing state (LOCKED, FREERUN, HOLDOVER)
  derived from process health, DPLL state, and PTP synchronization.
- **Clock Class**: The ITU-T clock quality indicator (6, 7, 135, 165, 248)
  announced to downstream clients via PMC and emitted as events.
- **Clock Mode**: The deployment topology (T-BC, T-TSC, T-GM, OC, BC) that
  determines which processes run and how failures are interpreted.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In T-BC / T-TSC mode, TR ptp4l process death while in
  LOCKED state results in holdover entry 100% of the time, with clock
  class 135 announced within 2 seconds. TR ptp4l death while in
  FREERUN/NOTSET results in FREERUN with clock class 248.

- **SC-002**: In T-BC mode, TT ptp4l process death results in freerun
  announcement 100% of the time, with recovery to locked within 5 seconds
  of ports returning to MASTER state.

- **SC-003**: When configured with a downtime threshold, phc2sys and
  ts2phc restarts within the threshold produce zero spurious state-change
  events.

- **SC-004**: ts2phc process restarts recover successfully 100% of the
  time (CNF-21499 resolved), with the process functional within 3 seconds.

- **SC-005**: In T-GM mode, ts2phc failure past the downtime threshold
  results in FREERUN state 100% of the time with freerun clock class (248)
  announced to match (no class 7 for that failure). ptp4l (TT) failure
  results in freerun clock class (248) announcement within 2 seconds.

- **SC-006**: All process failures across all clock modes (T-BC, T-GM,
  OC, BC) produce `PTP_PROCESS_STATUS` log lines within 1 second.

- **SC-007**: No false CLOCK_REALTIME synchronization events are emitted
  when the actual failure is in the upstream PTP source (TR ptp4l).

- **SC-008**: The system handles 10 consecutive process crash/restart
  cycles without entering a degraded state (no stuck holdover, no
  resource leaks, no stale monitoring).

- **SC-009**: During profile reconfiguration, after FREERUN (or
  equivalent) timing state and matching announcements have already been
  issued for the coordinated restart, a managed process failing to start
  produces **no duplicate** equivalent clock-state or clock-class
  announcements in 100% of observed test runs (FR-018).

## Assumptions

- The ptp-operator CR field for per-process downtime thresholds has been
  merged (ptp-operator PR #174) and is available for the daemon to read.
- The existing DPLL holdover mechanism is working correctly for link-down
  scenarios and can be reused for process-death-triggered holdover.
- The existing T-BC source-lost detection can be extended to treat TR
  ptp4l death as a source-lost condition.
- The cloud-event-proxy (or equivalent consumer) translates daemon log
  lines into CloudEvents; this spec covers the daemon's log/event
  behavior only.
- The MinOffsetThreshold setting is a candidate for deprecation (as noted
  in the team document) but deprecation is out of scope for this feature.
- The T-BC downstream clock class announcement mechanism already handles
  propagation correctly once the BC state machine provides the right
  clock class.
- synce4l and chronyd failure handling is explicitly out of scope. These
  processes operate independently of the PTP clock state machine and can
  be addressed in a follow-up feature if needed.

For detailed current implementation analysis, see
[current-behavior.md](./current-behavior.md).
