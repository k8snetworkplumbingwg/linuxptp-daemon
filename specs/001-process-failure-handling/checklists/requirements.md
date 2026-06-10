# Specification Quality Checklist: Process Failure Handling & Clock Class Announcement

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-04-18
**Last Validated**: 2026-04-27 (iteration 5 — FR-018 profile reconfiguration)
**Feature**: [spec.md](../spec.md)

**Legend**:
- ✅ Verified at spec stage
- ⚠️ Partially verified at spec stage, best-effort
- ❌ Verifiable after implementation only

## Content Quality

- ✅ No implementation details (languages, frameworks, APIs)
- ✅ Focused on user value and business needs
- ✅ Written for non-technical stakeholders
- ✅ All mandatory sections completed

## Requirement Completeness

- ✅ No [NEEDS CLARIFICATION] markers remain
- ✅ Requirements are testable and unambiguous — FR-009 vs FR-013 resolved; FR-017 threshold gating; FR-018 formalizes profile reconfiguration non-duplication (SC-009)
- ✅ Success criteria are measurable
- ✅ Success criteria are technology-agnostic (no implementation details)
- ⚠️ All acceptance scenarios are defined — FR-015 (gpspipe restart) and FR-016 (PTP_PROCESS_STATUS) lack dedicated scenarios; validated indirectly by SC-006 and SC-008
- ✅ Edge cases are identified
- ✅ Scope is clearly bounded
- ✅ Dependencies and assumptions identified

## Feature Readiness

- ⚠️ All functional requirements have clear acceptance criteria — FR-015 and FR-016 are cross-cutting requirements without dedicated user-story-level scenarios
- ⚠️ User scenarios cover primary flows — T-GM scenarios filled in from TBD; completeness best-effort until tested
- ❌ Feature meets measurable outcomes defined in Success Criteria — SCs exist and are measurable but cannot be validated until tests run
- ✅ No implementation details leak into specification

## Validation History

### Iteration 5 (2026-04-27)

| # | Severity | Issue | Resolution |
|---|----------|-------|------------|
| 1 | MEDIUM | Profile reconfiguration non-duplication was informal edge-case prose (“must NOT be duplicated!!!”) | Promoted to **FR-018**; edge case shortened with cross-ref; **SC-009** for verification |

### Iteration 4 (2026-04-27)

| # | Severity | Issue | Resolution |
|---|----------|-------|--------------|
| 1 | HIGH | FR-013 implied class 7 for any T-GM process outage; conflicted with FR-009 / Q4 (ts2phc → FREERUN + class 248 past threshold) | FR-013 split into holdover-capable (e.g. gpsd/gpspipe while ts2phc runs) vs holdover-breaking (ts2phc past threshold, TT ptp4l); explicit “not 7” when FREERUN |
| 2 | MEDIUM | FR-009 did not spell out pre-threshold inhibition of FREERUN / class downgrade | FR-009 expanded with MUST NOT transition/announce until threshold; cross-ref FR-017 |
| 3 | MEDIUM | Threshold suppressed “events” but state/class timing vs threshold was implicit | FR-017: no clock state, PMC class, or suppressed sync/GM-status changes solely from that outage until threshold; FR-006 exception; FR-016 unchanged |

### Iteration 3 (spec cleanup)

Code-level references (function names, file paths, internal data structures)
moved from spec.md to a separate
[current-behavior.md](../current-behavior.md) analysis document. Spec now
uses domain terms exclusively.

Removed from spec.md:
- `glog.Fatalf`, `CmdRun`, `applyNodePtpProfile`, `MonitorProcess`
- `isSourceLostBC`, `event_tbc.go`, `pkg/dpll/dpll.go`, `PtpSettings`
- `downstreamAnnounceIWF`, `mkFifo`
- "leaked goroutines", "orphaned DPLL watchers"

### Iteration 2 (consistency fixes)

| # | Severity | Issue | Resolution |
|---|----------|-------|------------|
| 1 | HIGH | SC-001 said "holdover 100%" but FR-006 specifies holdover only from LOCKED | Updated SC-001 to distinguish LOCKED→HOLDOVER vs FREERUN/NOTSET→FREERUN |
| 2 | HIGH | SC-005 said "reflect DPLL holdover state" contradicting Q4 (always FREERUN) | Updated SC-005 to state FREERUN for ts2phc failure, freerun class for ptp4l |
| 3 | LOW | US5 had duplicate scenario numbering (two "4." entries) | Renumbered to 1-7 |
| 4 | MEDIUM | FR-012 used vague "expected to restart quickly" | Changed to reference downtime threshold window explicitly |
| 5 | MEDIUM | FR-011 used vague "recover reliably" | Changed to measurable "within 3 seconds, 100% of the time" |

## Clarification Session Summary

5 questions asked and answered (2026-04-18):

1. **Timer lifecycle during crash loops** → Timer continues from first crash; 2s stabilization required within original window
2. **synce4l/chronyd scope** → Out of scope (independent of PTP clock state machine)
3. **OC/BC mode scope** → Deferred; FR-014 retained but implementation deferred to follow-up
4. **T-GM ts2phc failure state** → Always FREERUN (system can't hold over without ts2phc)
5. **TT ptp4l downstream communication** → Daemon-side log/metric only; clients detect via ANNOUNCE_RECEIPT_TIMEOUT

## Remaining Observations (Non-Blocking)

- FR-015 (gpspipe named pipe restart) and FR-016 (PTP_PROCESS_STATUS
  emission) do not have dedicated user-story-level acceptance scenarios.
  They are cross-cutting requirements validated by SC-006 and SC-008.
- FR-001/FR-002 use "ASAP" qualified by "(current behavior preserved)"
  which is acceptable since they describe existing behavior not being
  changed.
