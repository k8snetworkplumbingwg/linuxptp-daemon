# GNSS T-GM holdover

The GNSS-leading T-GM reports four composite states using existing metrics:

| Composite state | `clock_state` | `clock_class` | Time/frequency traceable |
| --- | --- | --- | --- |
| Locked | 1 | 6 | true/true |
| Holdover in spec | 2 | 7 | true/true |
| Holdover out of spec | 2 | 140 | false/true |
| Free run | 0 | 248 | false/false |

The metric labels and PTP lock-state enumeration are unchanged. A class 7 to
class 140 transition publishes a clock-class notification, not a false PTP
lock-state change. GNSS source loss does not override valid DPLL holdover.
GM announce settings are reconciled by one asynchronous worker per clock, not
by the locked event-processing path. Immutable desired settings coalesce to the
latest value. Reconciliation attempts start no more than once per second, each
with at most one GET and one SET; failures retry on an
independent exponential timer (1, 2, 4, ... up to 30 seconds), without needing
another telemetry event. Reset generations and desired revisions reject stale
GET results and SET acknowledgments. Supersession cancels active I/O, but a SET
already delivered to ptp4l cannot be recalled; the next reconciliation corrects
it. Removal, replacement and shutdown cancel and join transport cleanup outside
the clock manager lock. The GM transport uses cancellable, reaped PMC batch
processes with owned temporary local socket paths, cleaned after process exit,
and requires a complete response even if the process exits zero. Each attempt
refreshes the synchronized UTC-offset provider so a leap boundary does not
require another telemetry event to avoid restoring a stale UTC offset.

For modeled GNSS T-GM holdover, classes 7 and 140 advertise clock accuracy
Unknown (`0xFE`) and variance `0xffff`, as specified in T-GM sections 8.2/8.3.
The estimate is not a measured phase error: raw DPLL event offsets remain measured
values, or nil on hardware without that measurement. Non-model hardware retains
its existing accuracy behavior. Locked class 6 and free-run class 248 retain
their existing accuracy and variance policies.

## Limits and disabled holdover

Configuration defaults and hardware-specific overrides are resolved before
holdover eligibility is evaluated. For an eligible GNSS-leading T-GM:

- Resolved `LocalHoldoverTimeout == 0` disables holdover without evaluating
  an estimate. The instance model may remain unused. Actual hardware lock remains
  locked; attempted reference-loss holdover reports free run.
- With positive timeout, `LocalMaxHoldoverOffSet == 0` is an invalid active
  T-GM configuration. The receiving GM fails that attempted holdover closed to
  free run. The clock-neutral producer may still evaluate a zero-drift model;
  it does not apply GM admission policy to other clock types.
- `MaxInSpecOffset` may equal or exceed `LocalMaxHoldoverOffSet`.

While acquired holdover is active, a modeled error strictly greater than
`LocalMaxHoldoverOffSet` ends holdover. Otherwise an error strictly greater than
`MaxInSpecOffset`, or elapsed time strictly greater than `LocalHoldoverTimeout`,
ends **in-spec status**, not holdover itself. Free run takes precedence if both
conditions occur together. Equality is not exceedance. Out-of-spec quality is
latched until qualified recovery/reset, even if a later estimate is lower.

The linear estimate is the existing rounded drift model: the maximum offset
divided by timeout in seconds, multiplied by elapsed seconds. Consequently,
freerun and timeout thresholds are coupled, and equal offset limits may yield
a direct class 7 to class 248 transition. No artificial class 140 dwell is added.
Evaluation continues beyond the in-spec timeout to detect genuine freerun.

## Observation and interpretation boundary

DPLL owns hardware observation and reports raw hardware lock/holdover/fault,
measured phase offset, monotonic acquired reference-loss episodes and model
evaluation. It does not compare estimates with clock quality thresholds or
publish a modeled clock state. GM and TBC interpret a copy of each observation
before adding it to their event statistics. These receiving clocks own
qualification, holdover admission, thresholds, quality latches, effective state,
class, accuracy, traceability and offset reporting. A hardware HOLDOVER sample
can therefore coexist with a receiving clock already reporting free run.

`event.DPLLData.Holdover` is an additive in-process Go observation, not a new
IPC, protobuf or PMC wire message. Its fields are:

| Fields | Meaning |
| --- | --- |
| `HardwareState`, `MeasuredOffset` | Raw hardware state and signed measured phase offset; nil means unavailable |
| `ProducerID`, `Sequence`, `Episode` | Producer lifetime, increasing sample order and acquisition/loss episode identity |
| `Reference`, `PhaseOnly`, `HardwareSample` | Resolved source, phase-status-only exclusion and hardware versus estimator update |
| `Acquired`, `AcquisitionOffset`, `Active` | Hardware acquisition provenance, its measurement and active estimation episode |
| `Elapsed` | Monotonic elapsed episode time, independent of wall-clock timestamps |
| `EstimatedErrorNS`, `Valid`, `Error` | Nonnegative modeled error in nanoseconds, validity and evaluation diagnostic |
| `PeakErrorNS`, `ErrorObserved` | Cumulative peak/error observations within the episode, not threshold decisions |
| `TimeoutSeconds`, `MaxErrorNS`, `InSpecErrorNS` | Resolved configuration limits, interpreted by the receiving clock |

Cumulative facts let the receiver latch an observed threshold crossing or model
failure even if the bounded event channel drops an intermediate sample. They
cannot recover a physical transition never observed by DPLL. Receivers reject
older producers and duplicate/reordered sequences even if wall time moves
backward; reset fences the previously accepted episode until recovery or a new
producer. ClockManager retains known retired producer/episode identities per
config/interface across clock removal and replacement, so queued samples cannot
restore that retired episode in a new clock. This is an observed-sample fence,
not a registration protocol for a producer whose samples were never received.
Producer episode-generation checks reject in-flight results after recovery,
cancellation or a restarted episode. Clock-owned latches and cumulative facts
survive lower subsequent estimates within the same episode.

`DpllConfig.PTPState()` and `InSpec()` now describe raw hardware state and lock,
not inferred clock quality. Direct Go consumers requiring effective quality
must use receiving-clock state/statistics. Producer `DPLLData.State` is raw and
its `OutOfSpec`/`ModeledHoldover` are not quality decisions. Accepted clock stats
retain the observation alongside interpreted state/quality and clock-policy
offset inputs. `Clock.AddEvent` returns an `EventResult` with the accepted reporting
copy and common offset convention for ClockManager's existing logs and metrics.
The [generic reporting contract](event-reporting.md) also supports non-DPLL event
overrides and explicit suppression in the same processing result.
Events without `Holdover` retain their existing legacy interpretation for Go
callers that do not use the DPLL observation producer.
Externally visible metric labels, state enumeration, IPC and announce fields
are unchanged. There is no automatic hardware-model selector or wire schema
generation in this change.

## Model API and lifetime

`dpll.HoldoverModel.Estimate(time.Duration) (int64, error)` accepts the monotonic
elapsed duration of the current acquired reference-loss episode and returns a
nonnegative error estimate in integer nanoseconds. Both GNSS T-GM and legacy
PTP-leading T-BC DPLL observation workers use this interface. `NewDpll` selects
one model after configuration and constructor options are resolved, using the
shared default linear estimator when no model is injected. That model is fixed
for the instance lifetime and reused with each acquired episode's elapsed time.
The supplied linear model is the only production implementation.
`NewLinearHoldoverModel` validates positive inputs;
negative elapsed time and unrepresentable results are errors, not wrapped offsets.

`WithHoldoverModel` validates a supplied model and returns a per-instance
`dpll.Option` accepted by `NewDpll`. Handle its error before constructing the
DPLL. This constructor injection works for either consumer; it does not change
clock eligibility. A future hardware-derived model can be supplied at this
resolved configuration/hardware boundary, after HardwareConfig overrides or
legacy plugin/profile settings have been resolved. There is no automatic
hardware selector, new model implementation or global factory in this change.

An implementation must be an immutable, concurrency-safe, bounded computation.
It must not own a clock, timer, device I/O, goroutine or episode state, nor call
back into the DPLL. Measurement availability is independent of estimation:
`FlagNoPhaseOffset` does not bypass model decisions, and unavailable measured
offsets remain absent in DPLL events. No new measured-error source or SyncE
dependency is introduced.

The DPLL observation worker serializes evaluations without holding its state mutex while calling
the model. Before and after evaluation, it checks the active episode generation.
In-flight results **and errors** from an ended episode are discarded after
recovery, stop or reset, including when a new episode has started on the same
instance. A current active model
error is an observation that the receiving clock fails closed with a diagnostic.
A model is never evaluated to
make holdover decisions while the hardware is actually locked.

Resolved holdover configuration is fixed for each DPLL instance; configuration
reapplication constructs a new instance rather than mutating an active model's
thresholds or duration.

Hardware reacquisition stops the old episode and clears its elapsed/error
bookkeeping; the GM applies measurement qualification before restoring class 6.
A subsequent source loss starts at elapsed zero.

## Separate legacy receiving-clock policy

Sharing the estimator does not select T-GM policy for T-BC. Legacy
PTP-leading receiving TBC clocks still report the estimated value as offset,
retain the in-spec latch and class 135/165 policy, and exit to free run on their
hard holdover timeout. DPLL itself keeps the raw measured/nil offset separate.
The T-GM timeout instead ends in-spec status. Both receiving clocks share the
same final reporting convention for DPLL observations: a locked offset is the
signed measurement; a valid modeled HOLDOVER estimate replaces an already
present offset with a nonnegative estimated error; interpreted FREERUN replaces
a present offset with `FaultyPhaseOffset` (`99999999999`). An absent offset stays
nil in every state, including valid modeled holdover. An invalid estimate that
remains in HOLDOVER retains the existing offset; error paths interpreted as
FREERUN use the terminal sentinel. Logs and `openshift_ptp_offset_ns` use this
same state-dependent convention. Reporting copies the accepted view and leaves
raw observations and stored clock-policy inputs unchanged.
No T-GM class 7/140 or Unknown-accuracy policy is applied
to T-BC. PPS followers, hardware-slaved `FlagOnlyPhaseStatus` devices and BC/OC
policy remain unchanged. Disabled holdover does not evaluate a model.

Legacy zero maximum offset with positive timeout retains its zero-drift estimate;
it is not given a positive default or T-GM's invalid-limit admission policy.
Legacy timeout conversion and hard-exit behavior are unchanged, including the
existing duration conversion for very large unsigned second limits. Default
linear selection uses the original unsigned seconds directly, without narrowing
them to `time.Duration` for drift calculation. A model error, negative result or
unrepresentable linear result terminates the active legacy attempt as free run
with a diagnostic rather than publishing a wrapped offset. The shared estimator
does not run while locked or for downstream/nonapplicable DPLLs. Both modes use
the cancellable, episode-generation-fenced DPLL observation worker outside the
state mutex. It continues observing after a receiver's terminal quality decision
until hardware recovery/fault or process stop, without restarting that clock's
latched holdover. Stop joins the worker; arbitrary blocking model implementations
violate the bounded no-I/O model contract and cannot be forcibly interrupted.
Reconfigure/reconstruct the DPLL to select a different model.

Legacy `Slope()` and `Timer()` values remain configuration-derived linear
diagnostics. They do not describe an injected nonlinear model or control its
offset estimates; the hard timeout still uses `LocalHoldoverTimeout`.

The public behavioral reference is the [T-GM specification](https://github.com/ptp-spec-driven/clock-requirements/blob/main/tgm/spec.md),
especially sections 6.2, 6.4, 6.8, 8 and 12.5.

## Managed-worker retirement

The manager uses the optional `clock.ManagedWorkerOwner` capability, not PMC
operations. `StopManagedWorkers() <-chan struct{}` terminally stops only the
owner's explicitly managed workers; the returned channel closes on their
completion. Calls are serialized with other clock operations. GM owns its PMC
reconciler cancellation, including idempotent stop before first use and prevention
of restart after stop. `Reset` invalidates pending work but remains nonterminal.
Replacement activation waits for managed completion outside the event lock while
lifecycle operations remain serialized. A delivered SET cannot be recalled.
This is not all-clock shutdown: legacy T-BC PMC goroutines, BC timers and HA
child/background work are untracked by this capability and are not drained.
Those clocks do not acquire no-op lifecycle methods or shared PMC transport.

## PMC reconciliation naming

The `pmcReconciler` worker names the PMC scheduling, cancellation,
supersession and retry mechanism independently of clock type. Its current
`gmDesired` inputs and `gmSettings` builder still implement GM-specific
policy. Only GM clocks use this worker; T-BC reconciliation remains
separate. Neutral mechanism names do not imply shared clock policy or
new T-BC support. The PMC GRANDMASTER_SETTINGS_NP message and existing
GMSettings client APIs retain their protocol names.
