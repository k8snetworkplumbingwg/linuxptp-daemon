# Dual Time Receiver Ports — Proof of Concept Plan

**Date:** 2026-05-17
**Status:** Draft
**Epic:** Enable dual time receiver ports (same NIC) for boundary clocks (BC / T-BC)

---

## 1. Objective

Validate that dual time receiver (TR) ports on a boundary clock correctly fail over
between two upstream PTP paths using ptp4l A-BMCA, with:

- **Per-port state tracking** in the linuxptp-daemon (aggregate holdover only when ALL
  upstream ports are lost — no false holdover on single-port failure)
- **phc2sys stability** through switchover and holdover
- **DPLL control and monitoring orchestration** via HardwareConfig

**Stretch goal — A-BMCA internal vs external clock comparison:** Validate that ptp4l
compares external source quality against the internal clock (holdover class 135), not
just against other external sources. This behavior is not specific to dual-TR — it
applies to single-TR as well — but the dual-TR PoC provides a good opportunity to
verify it. Covered by exploratory scenarios S2b and S4b.

## 2. Scope

### Two Phases — WPC First

| Phase | GM Node | DUT Node | Configs Tested |
|-------|---------|----------|----------------|
| **Phase 1: WPC E810** | WPC E810-XXVDA4T (2 ptp4l instances) | WPC E810-XXVDA4T | T-BC + HwConfig |
| **Phase 2: GNR-D** | WPC E810-XXVDA4T (2 ptp4l instances) | GNR-D (Dell XR8720t) | T-BC + HwConfig (GNR-D E825 DPLL) |

### Out of Scope

- TR ports on different NICs (all TR ports must share the same PHC)
- Non-Intel platform HardwareConfig/DPLL support
- phc2sys automatic mode (`-a`) for dual-TR — use `-s <main-TR-port>` instead

---

## 3. Lab Topology

### 3.1 GM Node Setup

A single WPC E810-XXVDA4T node runs **two independent ptp4l instances**, one per
downstream port. Both instances are disciplined by the same PHC (same NIC) via
ts2phc/GNSS, so they provide identical baseline time quality. Each instance can be
independently controlled via `pmc` to simulate path-specific quality degradation or
clock class changes.

### 3.2 Diagram — Phase 1: WPC E810 → WPC E810

```
  ┌──────────────────────────────────────────────┐
  │         GM Node — Intel WPC E810-XXVDA4T     │
  │                                              │
  │  ┌────────────────┐   ┌────────────────┐     │
  │  │ ptp4l inst-A   │   │ ptp4l inst-B   │     │
  │  │ clockClass 6   │   │ clockClass 6   │     │
  │  │ domain 24      │   │ domain 24      │     │
  │  │ G.8275.x BMCA  │   │ G.8275.x BMCA  │     │
  │  │                │   │                │     │
  │  │ Port: ens4f0   │   │ Port: ens4f1   │     │
  │  │ masterOnly 1   │   │ masterOnly 1   │     │
  │  └───────┬────────┘   └───────┬────────┘     │
  │          │     same PHC       │              │
  │          │                    │              │
  │          │  ┌──────────────┐  │              │
  │          └──┤ ts2phc/GNSS  ├──┘              │
  │             └──────────────┘                 │
  └──────────────┼────────────────┼──────────────┘
                 │  P2P L2/PTP   │  P2P L2/PTP
                 ▼               ▼
  ┌──────────────────────────────────────────────┐
  │       DUT Node — Intel WPC E810-XXVDA4T      │
  │                                              │
  │  ┌────────────────────────────────────┐      │
  │  │         ptp4l (single instance)    │      │
  │  │                                    │      │
  │  │  ens4f0 ── TR  (localPriority 100) │      │
  │  │  ens4f1 ── TR  (localPriority 200) │      │
  │  │  ens4f2 ── TT  (masterOnly 1)      │      │
  │  │  ens4f3 ── TT  (masterOnly 1)      │      │
  │  │                                    │      │
  │  │  A-BMCA: G.8275.x                  │      │
  │  │  clock_class_threshold: 135        │      │
  │  └───────────────┬────────────────────┘      │
  │                  │                           │
  │         ┌────────┴────────┐                  │
  │         │    phc2sys      │                  │
  │         │  -s ens4f0      │                  │
  │         │  -r -n 24       │                  │
  │         └─────────────────┘                  │
  └──────────────────────────────────────────────┘
```

### 3.3 Diagram — Phase 2: WPC E810 → GNR-D

```
  ┌──────────────────────────────────────────────┐
  │         GM Node — Intel WPC E810-XXVDA4T     │
  │                                              │
  │  ┌────────────────┐   ┌────────────────┐     │
  │  │ ptp4l inst-A   │   │ ptp4l inst-B   │     │
  │  │ clockClass 6   │   │ clockClass 6   │     │
  │  │ domain 24      │   │ domain 24      │     │
  │  │ G.8275.x BMCA  │   │ G.8275.x BMCA  │     │
  │  │                │   │                │     │
  │  │ Port: ens4f0   │   │ Port: ens4f1   │     │
  │  │ masterOnly 1   │   │ masterOnly 1   │     │
  │  └───────┬────────┘   └───────┬────────┘     │
  │          │     same PHC       │              │
  │          │                    │              │
  │          │  ┌──────────────┐  │              │
  │          └──┤ ts2phc/GNSS  ├──┘              │
  │             └──────────────┘                 │
  └──────────────┼────────────────┼──────────────┘
                 │  P2P L2/PTP   │  P2P L2/PTP
                 ▼               ▼
  ┌──────────────────────────────────────────────┐
  │      DUT Node — GNR-D (Dell XR8720t)         │
  │      Intel E825 subsystem                    │
  │                                              │
  │  ┌────────────────────────────────────┐      │
  │  │         ptp4l (single instance)    │      │
  │  │                                    │      │
  │  │  eno2  ── TR  (localPriority 100)  │      │
  │  │  eno3  ── TR  (localPriority 200)  │      │
  │  │  eno4  ── TT  (masterOnly 1)       │      │
  │  │                                    │      │
  │  │  A-BMCA: G.8275.x                  │      │
  │  │  clock_class_threshold: 135        │      │
  │  └───────────────┬────────────────────┘      │
  │                  │                           │
  │  ┌───────────────┴────────────────────┐      │
  │  │   DPLL via eno5 (E825 leader)      │      │
  │  │   (HardwareConfig)                 │      │
  │  └───────────────┬────────────────────┘      │
  │                  │                           │
  │         ┌────────┴────────┐                  │
  │         │    phc2sys      │                  │
  │         │  -s eno2        │                  │
  │         │  -r -n 24       │                  │
  │         └─────────────────┘                  │
  └──────────────────────────────────────────────┘
```

### 3.4 GM Clock Class Control via pmc

Each GM ptp4l instance runs on a separate port with its own UDS socket. To control
which upstream path the DUT selects as active, set different `priority2` values on
each GM instance (lower = preferred by A-BMCA). To simulate quality degradation,
manipulate the `clockClass` via `pmc`:

```bash
# Degrade GM-A (inst-A) to clockClass 165:
pmc -u -b 0 -f /var/run/ptp4l.0.socket \
  'SET GRANDMASTER_SETTINGS_NP clockClass 165 clockAccuracy 0x21 \
   offsetScaledLogVariance 0x4e5d currentUtcOffset 37 leap61 0 \
   leap59 0 currentUtcOffsetValid 1 ptpTimescale 1 \
   timeTraceable 1 frequencyTraceable 1 timeSource 0x20'

# Restore GM-A to clockClass 6:
pmc -u -b 0 -f /var/run/ptp4l.0.socket \
  'SET GRANDMASTER_SETTINGS_NP clockClass 6 clockAccuracy 0x21 \
   offsetScaledLogVariance 0x4e5d currentUtcOffset 37 leap61 0 \
   leap59 0 currentUtcOffsetValid 1 ptpTimescale 1 \
   timeTraceable 1 frequencyTraceable 1 timeSource 0x20'
```

---

## 4. Phase 1 Test Matrix — WPC E810 DUT

| Test ID | DUT PtpConfig | HardwareConfig | What It Validates |
|---------|---------------|----------------|-------------------|
| **P1-A** | `poc-e810-tbc-dual-tr.yaml` | `poc-e810-hwconfig-dual-tr.yaml` | T-BC with full HwConfig orchestration: DPLL + per-port state + offset filtering |

---

## 5. Test Scenarios

### Pre-conditions (all scenarios)

Before starting each scenario, verify:

1. DUT PtpConfig is applied and ptp4l is running
2. One TR port is in SLAVE state (the preferred port — localPriority 100 for T-BC)
3. The other TR port is in PASSIVE
4. phc2sys is running with `-s <main-TR-port>` (e.g., `-s ens4f0`) — verify no `-a` flag
5. Downstream TT ports are in MASTER state
6. Both GM ptp4l instances are running, announcing clockClass 6

```bash
# Verify DUT port states:
oc logs -n openshift-ptp <daemon-pod> -c linuxptp-daemon-container | \
  grep -E "(to SLAVE|to PASSIVE|to MASTER|to LISTENING)" | tail -10

# Verify phc2sys is running with interface name (not -a):
oc logs -n openshift-ptp <daemon-pod> -c linuxptp-daemon-container | \
  grep "phc2sys" | grep "\-s" | tail -5

# Verify GM instances are announcing clockClass 6:
# On GM node:
pmc -u -b 0 -f /var/run/ptp4l.0.socket 'GET GRANDMASTER_SETTINGS_NP'
pmc -u -b 0 -f /var/run/ptp4l.1.socket 'GET GRANDMASTER_SETTINGS_NP'
```

---

### Scenario 1 — Active upstream port disconnection

**Purpose:** Validate that when the active upstream path is lost due to physical
disconnection, the DUT re-selects the backup upstream path — provided the backup
source quality is better than the internal clock.

#### Steps

| Step | Action | Expected Result |
|------|--------|-----------------|
| 1.1 | Identify the active TR port | Grep logs for `"to SLAVE on MASTER_CLOCK_SELECTED"`. The active path can be controlled by setting different `priority2` values on each GM ptp4l instance (lower value = preferred by A-BMCA). |
| 1.2 | **Disconnect the active upstream path** | Physical cable pull, or `sudo ip link set <GM-port> down` on the GM node (e.g., `ip link set ens2f0 down` if ens2f0 is the GM peer of the DUT's active TR port) |
| 1.3 | ptp4l detects loss | Active port: `"SLAVE to FAULTY"` within ~50ms (`tx_timestamp_timeout 50`) |
| 1.4 | A-BMCA evaluates backup vs internal | Backup port's GM is clockClass 6 (better than internal holdover class 135). A-BMCA selects backup. |
| 1.5 | BMCA promotes backup port | Backup port: `"PASSIVE to UNCALIBRATED on RS_SLAVE"` then `"UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED"` |
| 1.6 | Daemon per-port state | Log: `"BC port ens4f0 lost SLAVE"` then `"BC port ens4f1 LOCKED"` |
| 1.7 | Brief holdover during BMCA gap | Log may show `"all upstream ports lost"` briefly while BMCA promotes backup port. Must be followed by `"BC port <backup> LOCKED"` and holdover exit once backup reaches SLAVE. |
| 1.8 | DPLL behavior | Brief holdover during BMCA gap (~5-7s: UNCALIBRATED→SLAVE ~1-3 sync intervals + offset filter fill ~4s). Then DPLL re-locks. Log: `"T-BC MOVE TO NORMAL STATE"` |
| 1.9 | phc2sys continuity | phc2sys continues on main TR port — no restart, no error, system clock updated throughout |
| 1.10 | Downstream TT ports | Remain in MASTER state throughout the switchover |

#### Pass Criteria

- Backup port becomes SLAVE within 30s of active port failure
- phc2sys is NOT restarted
- Brief holdover during the BMCA gap is expected (DPLL not disciplined by PTP until backup port locks). DUT must exit holdover once backup reaches SLAVE.
- Downstream TT ports remain stable in MASTER

---

### Scenario 2 — Active upstream quality degradation with A-BMCA internal clock comparison

**Purpose:** Validate two sub-cases of quality degradation:
- **S2a:** Active path degrades but backup path has better quality than internal clock
  → DUT switches to backup
- **S2b:** Active path degrades AND backup path also has worse quality than the internal
  clock in holdover → DUT remains in holdover (does NOT lock to degraded external source)

This validates that A-BMCA compares external sources against the internal clock
(holdover class 135, controlled by `clock_class_threshold`), not just against each other.

#### S2a — Backup path has better quality than internal clock

| Step | Action | Expected Result |
|------|--------|-----------------|
| 2a.1 | Verify DUT steady state | ens4f0 is SLAVE (localPriority 100), ens4f1 is PASSIVE |
| 2a.2 | **Degrade GM-A to clockClass 165** | `pmc -u -b 0 -f /var/run/ptp4l.0.socket 'SET GRANDMASTER_SETTINGS_NP clockClass 165 ...'` |
| 2a.3 | A-BMCA re-evaluates | DUT detects degraded announce from GM-A (clockClass 165). GM-B is still clockClass 6. |
| 2a.4 | A-BMCA comparison | GM-B (class 6) is better than GM-A (class 165) AND better than internal clock (class 135 threshold). A-BMCA selects GM-B. |
| 2a.5 | Port switchover | ens4f1: `"PASSIVE → UNCALIBRATED → SLAVE"`. ens4f0: `"SLAVE → PASSIVE"` or `"SLAVE → LISTENING"` |
| 2a.6 | Daemon per-port tracking | Per-port state updates. Brief holdover possible during BMCA gap until backup port reaches SLAVE. |
| 2a.7 | phc2sys | Unaffected — still syncing from main TR port |
| 2a.8 | Restore GM-A to clockClass 6 | `pmc` restore. A-BMCA may switch back to ens4f0 (lower localPriority). |

**Pass criteria:** DUT switches to backup port. No holdover. phc2sys stable.

#### S2b — Both external sources worse than internal clock (EXPLORATORY)

> **Note:** This scenario is **exploratory**. The design spec (Section 8 — Out of
> Scope) defers the A-BMCA internal-vs-external clock class awareness to a separate
> epic. The behavior tested here relies on ptp4l's `clock_class_threshold` parameter,
> not on A-BMCA logic itself. The purpose of this scenario is to **determine whether
> `clock_class_threshold 135` achieves the desired rejection** of degraded sources.
> If ptp4l does NOT reject the external sources, document the actual behavior — this
> informs whether additional logic is needed in the daemon or ptp4l configuration.

| Step | Action | Expected Result |
|------|--------|-----------------|
| 2b.1 | Verify DUT steady state | ens4f0 is SLAVE, ens4f1 is PASSIVE |
| 2b.2 | **Degrade GM-A to clockClass 165** | `pmc` on inst-A socket |
| 2b.3 | **Degrade GM-B to clockClass 165** | `pmc -u -b 0 -f /var/run/ptp4l.1.socket 'SET GRANDMASTER_SETTINGS_NP clockClass 165 ...'` |
| 2b.4 | A-BMCA re-evaluates | Both external sources are clockClass 165. Internal clock threshold is 135. |
| 2b.5 | **Observe: does ptp4l reject both external sources?** | **Expected (if clock_class_threshold works):** clockClass 165 > threshold 135, ptp4l rejects both. Both TR ports leave SLAVE. **Alternative (if threshold does not apply here):** ptp4l may keep one port in SLAVE because 165 < 248 (default clockClass). Document actual behavior. |
| 2b.6 | If both rejected → DUT enters holdover | Log: `"all upstream ports lost - MOVE TO HOLDOVER"` |
| 2b.7 | DPLL holdover | DPLL enters hardware holdover. Plugin hook `tbc-ho-entry` fired. |
| 2b.8 | phc2sys | Continues on main TR port. PHC hardware-disciplined in holdover. |
| 2b.9 | **Restore GM-A to clockClass 6** | `pmc` on inst-A socket |
| 2b.10 | A-BMCA re-evaluates | GM-A (class 6) is now better than internal clock (class 135). A-BMCA selects GM-A. |
| 2b.11 | Recovery | ens4f0 transitions to SLAVE. Daemon exits holdover. |

**Expected outcome:** DUT does NOT lock to clockClass 165 sources when internal
holdover clock (class 135) is better. DUT enters holdover instead. DUT recovers when
a source with quality better than internal clock becomes available.

**If ptp4l does NOT reject the degraded sources:** Document the actual behavior and
the conclusion that additional logic (either in ptp4l configuration or in the daemon)
is needed to handle this case. This finding feeds into the deferred epic.

---

### Scenario 3 — Both upstream paths lost (holdover entry)

**Purpose:** Validate that the DUT enters holdover when both upstream paths are
physically lost, and that holdover is correctly detected only after ALL ports lose
SLAVE state.

#### Steps

| Step | Action | Expected Result |
|------|--------|-----------------|
| 3.1 | Verify DUT steady state | One TR port is SLAVE |
| 3.2 | **Disconnect both upstream paths simultaneously** | Physical cable pull on both ports, or `sudo ip link set <GM-port-A> down && sudo ip link set <GM-port-B> down` on the GM node. If simultaneous disconnect is not feasible, bring down the backup GM port first (DUT backup port is PASSIVE, no state change), then the active GM port. |
| 3.3 | Both ports lose upstream connectivity | Active port loses SLAVE. Backup port never receives announces to trigger BMCA promotion. |
| 3.4 | Both ports lost | Both TR ports: `"to FAULTY"` (with `ip link set down`) or `"to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES"` / `"to LISTENING"` (with cable pull) |
| 3.5 | Aggregate holdover detection | Log: `"BC all upstream ports lost - MOVE TO HOLDOVER"` |
| 3.6 | DPLL holdover | DPLL enters hardware holdover. Log: `"tbc-ho-entry"` plugin hook fired. |
| 3.7 | phc2sys behavior | Continues running on main TR port. PHC hardware-disciplined in holdover, system clock gets holdover-quality time. |
| 3.8 | Downstream TT ports | Remain in MASTER state — still serving time (holdover quality). DUT clockClass changes to 135 (holdover). |

#### Pass Criteria

- Holdover detected ONLY after BOTH ports lost — not on single-port failure
- DPLL enters hardware holdover, plugin hook `tbc-ho-entry` fired
- phc2sys continues running
- DUT clockClass advertised to downstream changes to holdover class (135)

---

### Scenario 4 — Recovery from holdover with A-BMCA internal clock comparison

**Purpose:** Validate that the DUT recovers from holdover and synchronizes to the
upstream clock ONLY IF the upstream clock quality is better than the local clock's
holdover quality. If the recovering source has worse quality than the internal
holdover clock, the DUT must remain in holdover.

#### S4a — Upstream source quality is better than internal holdover clock

| Step | Action | Expected Result |
|------|--------|-----------------|
| 4a.1 | Start from holdover state (Scenario 3) | Both ports disconnected, DUT in holdover, clockClass 135 |
| 4a.2 | **Reconnect one upstream path** (e.g., to GM-B, clockClass 6) | Reconnect cable or `sudo ip link set <GM-port-B> up`. GM-B is announcing clockClass 6, which is better than internal holdover class 135. |
| 4a.3 | ptp4l detects announces | Port transitions: `"LISTENING → UNCALIBRATED on RS_SLAVE"` → `"UNCALIBRATED → SLAVE on MASTER_CLOCK_SELECTED"` |
| 4a.4 | A-BMCA comparison | GM-B clockClass 6 < clock_class_threshold 135. A-BMCA accepts external source. |
| 4a.5 | Daemon recovery | Log: `"BC port ens4f1 LOCKED"`. `allPortsLost()` returns false. |
| 4a.6 | DPLL recovery | DPLL exits holdover after offset filter stabilizes (~4s). Log: `"T-BC MOVE TO NORMAL STATE"`. Plugin hook `tbc-ho-exit` fired. |
| 4a.7 | phc2sys | Continues uninterrupted. System clock now receiving accurate time again. |
| 4a.8 | Reconnect second upstream path | Reconnect cable or `ip link set up`. Second port becomes PASSIVE — no dual-SLAVE condition. |
| 4a.9 | DUT clockClass | Changes back from 135 (holdover) to 6 (locked to external source) |

**Pass criteria:** Clean exit from holdover. No dual-SLAVE. phc2sys unaffected. DUT
clockClass restored.

#### S4b — Upstream source quality is worse than internal holdover clock (EXPLORATORY)

> **Note:** Same caveat as S2b — this is an **exploratory scenario** testing whether
> `clock_class_threshold` prevents ptp4l from locking to a degraded source during
> holdover. The design spec defers this as an open question. Document actual behavior.

| Step | Action | Expected Result |
|------|--------|-----------------|
| 4b.1 | Start from holdover state (Scenario 3) | Both ports disconnected, DUT in holdover, clockClass 135 |
| 4b.2 | **On GM-B, set clockClass to 165** | `pmc -u -b 0 -f /var/run/ptp4l.1.socket 'SET GRANDMASTER_SETTINGS_NP clockClass 165 ...'` |
| 4b.3 | **Reconnect upstream path to GM-B** | Reconnect cable or `sudo ip link set <GM-port-B> up`. GM-B is announcing clockClass 165. |
| 4b.4 | **Observe: does ptp4l reject the degraded source?** | **Expected (if clock_class_threshold works):** clockClass 165 > threshold 135. ptp4l rejects. Port does NOT transition to SLAVE. DUT remains in holdover. **Alternative:** ptp4l may accept the source (165 < 248). Document actual behavior. |
| 4b.5 | If rejected → DUT remains in holdover | Log: no `"to SLAVE"` for the reconnected port |
| 4b.6 | phc2sys | Continues on main TR port in holdover mode |
| 4b.7 | **Restore GM-B to clockClass 6** | `pmc` on inst-B socket |
| 4b.8 | A-BMCA re-evaluates | GM-B clockClass 6 < threshold 135. A-BMCA accepts. |
| 4b.9 | DUT recovers | Port transitions to SLAVE. Daemon exits holdover. |

**Expected outcome:** DUT does NOT lock to clockClass 165 source while in holdover
(internal clock at class 135 is better). DUT recovers only when source quality
improves above the threshold.

**If ptp4l accepts the degraded source:** Document this as a finding. It means
`clock_class_threshold` does not prevent locking to a source with clockClass between
the threshold and the default (248), and additional logic is needed.

---

## 6. Phase 2 Test Matrix — GNR-D DUT

| Test ID | DUT PtpConfig | HardwareConfig | What It Validates |
|---------|---------------|----------------|-------------------|
| **P2-A** | `poc-gnrd-tbc-dual-tr.yaml` | `poc-gnrd-hwconfig-dual-tr.yaml` | T-BC with GNR-D E825 DPLL + HardwareConfig |

Same scenarios (S1, S2a, S2b, S3, S4a, S4b) executed on GNR-D DUT.

Key differences to observe vs Phase 1:
- GNR-D uses E825 DPLL (different pin labels: `GNR-D_SDP0`, `GNSS_1PPS_IN`)
- DPLL leader interface is `eno5` (not one of the TR ports)
- TR ports are `eno2` and `eno3` (interface names vary by firmware/BIOS — verify
  with `ip link` and `ethtool -T` on target hardware)
- Different holdover oscillator characteristics (E825 vs E810 TCXO)

---

## 7. Measurements and Observability

### 7.1 Switchover Time

Measure the time between the active port losing SLAVE and the backup port entering
SLAVE. Extract from ptp4l log timestamps:

```bash
# Timestamp of active port losing SLAVE:
oc logs -n openshift-ptp <daemon-pod> -c linuxptp-daemon-container | \
  grep "ens4f0.*SLAVE to" | tail -1

# Timestamp of backup port entering SLAVE:
oc logs -n openshift-ptp <daemon-pod> -c linuxptp-daemon-container | \
  grep "ens4f1.*to SLAVE on MASTER_CLOCK_SELECTED" | tail -1

# Delta = switchover time. Target: < 10 seconds.
```

### 7.2 Holdover Duration

For scenarios where DPLL enters holdover (P1-A, P2-A), measure the holdover window:

```bash
# Holdover entry:
oc logs ... | grep "MOVE TO HOLDOVER" | tail -1

# Holdover exit:
oc logs ... | grep "MOVE TO NORMAL STATE" | tail -1

# Delta = holdover duration during switchover.
# Expected: ~6-8s (BMCA timeout + offset filter fill).
# For E810 TCXO (~1.5ppm): worst-case drift ~12ns during this window.
```

### 7.3 phc2sys Continuity

Verify phc2sys was not restarted during any scenario:

```bash
# Check phc2sys process start count (should be 1 throughout all scenarios):
oc logs -n openshift-ptp <daemon-pod> -c linuxptp-daemon-container | \
  grep -c "phc2sys.*started"

# Verify phc2sys offset is being reported throughout:
oc logs -n openshift-ptp <daemon-pod> -c linuxptp-daemon-container | \
  grep "phc2sys.*offset" | tail -20
```

### 7.4 DUT ClockClass Advertised Downstream

Verify the DUT's clockClass changes correctly during holdover:

```bash
# On a downstream device or via pmc on DUT:
pmc -u -b 0 'GET GRANDMASTER_SETTINGS_NP'
# During normal operation: clockClass 6
# During holdover: clockClass 135
# After recovery: clockClass 6
```

---

## 8. DUT Configuration Files

### Phase 1 — WPC E810

| Config | File | Notes |
|--------|------|-------|
| P1-A PtpConfig | `poc-e810-tbc-dual-tr.yaml` | T-BC with upstreamPort: ens4f0,ens4f1 |
| P1-A HardwareConfig | `poc-e810-hwconfig-dual-tr.yaml` | DPLL conditions: init, locked, lost |

**Note on phc2sysOpts:** All PoC config YAMLs use `-s <main-TR-port>` (the primary
upstream interface name, e.g., `-s ens4f0`).

### Phase 2 — GNR-D

| Config | File | Notes |
|--------|------|-------|
| P2-A PtpConfig | `poc-gnrd-tbc-dual-tr.yaml` | T-BC with upstreamPort: eno2,eno3 |
| P2-A HardwareConfig | `poc-gnrd-hwconfig-dual-tr.yaml` | GNR-D DPLL conditions via eno5 leader |

---

## 9. Expected PoC Results Summary

| Result | How We Validate | Applies To |
|--------|-----------------|------------|
| A-BMCA correctly selects best upstream port | Port state transitions in ptp4l logs | P1-A, P2-A |
| A-BMCA compares external vs internal clock | DUT stays in holdover when external source is worse than holdover class (S2b, S4b) | P1-A, P2-A |
| A-BMCA recovers only when external > internal | DUT locks to external source only when clockClass < threshold 135 (S4a) | P1-A, P2-A |
| phc2sys on main TR port stable through all transitions | phc2sys not restarted, no errors, system clock updated | P1-A, P2-A |
| Aggregate holdover detection | Brief holdover during BMCA gap on single-port failure (S1, S2a), full holdover only when all ports lost (S3) | P1-A, P2-A |
| DPLL holdover/recovery | DPLL pin conditions change on entry/exit, holdover offset within spec | P1-A, P2-A |
| Plugin hooks | `tbc-ho-entry`/`tbc-ho-exit` called on holdover entry/exit | P1-A, P2-A |
| Switchover time | < 10s from active port loss to backup port SLAVE | P1-A, P2-A |
| DUT clockClass reflects state | Class 6 when locked, class 135 in holdover | P1-A, P2-A |
| Clean recovery, no dual-SLAVE | Only one port in SLAVE after recovery | P1-A, P2-A |

---

## 10. Known Limitations and Caveats

1. **clock_class_threshold behavior:** The `clock_class_threshold 135` setting in ptp4l
   controls whether ptp4l accepts an external source. When the DUT is in holdover and
   its own clockClass is 135, ptp4l should reject sources with clockClass > 135.
   This is the mechanism that prevents locking to degraded sources. The PoC must verify
   this works correctly in practice.

2. **DPLL brief holdover during switchover:** Even in the single-port-failure case (S1),
   the DPLL will briefly enter holdover during the BMCA gap. This is by design — the
   DPLL is not being disciplined by PTP during that ~6-8s window. The holdover drift
   during this gap is negligible for E810 TCXO (~12ns worst case).

3. **phc2sys must use `-s <main-TR-port>` (interface name):**

4. **GM node PtpConfig:** The GM node configuration (two independent ptp4l instances
   on separate ports) is not provided in this document. The tester configures the GM
   node with two profiles, each mastering on a separate E810 port, sharing the same
   PHC via ts2phc/GNSS.
