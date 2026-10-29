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
Unchanged-state traceability settings are verified at most once per second;
class/accuracy changes and failed corrections bypass that bound.

## Limits and disabled holdover

Configuration defaults and hardware-specific overrides are resolved before
holdover eligibility is evaluated. For an eligible GNSS-leading T-GM:

- Resolved `LocalHoldoverTimeout == 0` disables holdover without constructing
  or evaluating an estimate. Actual hardware lock remains locked; attempted
  reference-loss holdover reports free run.
- With positive timeout, `LocalMaxHoldoverOffSet == 0` is an invalid active
  linear-model configuration. An error is reported when holdover is attempted,
  and that attempt fails closed to free run without constructing the model.
- `MaxInSpecOffset` may equal or exceed `LocalMaxHoldoverOffSet`.

While acquired holdover is active, a modeled error strictly greater than
`LocalMaxHoldoverOffSet` ends holdover. Otherwise an error strictly greater than
`MaxInSpecOffset`, or elapsed time strictly greater than `LocalHoldoverTimeout`,
ends **in-spec status**, not holdover itself. Free run takes precedence if both
conditions occur together. Equality is not exceedance. Out-of-spec quality is
latched until qualified recovery/reset, even if a replacement estimate is lower.

The linear estimate is the existing rounded drift model: the maximum offset
divided by timeout in seconds, multiplied by elapsed seconds. Consequently,
freerun and timeout thresholds are coupled, and equal offset limits may yield
a direct class 7 to class 248 transition. No artificial class 140 dwell is added.
Evaluation continues beyond the in-spec timeout to detect genuine freerun.

## Model API and lifetime

`dpll.HoldoverModel.Estimate(time.Duration) (int64, error)` accepts the monotonic
elapsed duration of the current acquired reference-loss episode and returns a
nonnegative error estimate in integer nanoseconds. The supplied linear model is
the only production implementation. Its constructor validates positive inputs;
negative elapsed time and unrepresentable results are errors, not wrapped offsets.

An implementation must be an immutable, concurrency-safe, bounded computation.
It must not own a clock, timer, device I/O, goroutine or episode state, nor call
back into the DPLL. Measurement availability is independent of estimation:
`FlagNoPhaseOffset` does not bypass model decisions, and unavailable measured
offsets remain absent in DPLL events. No new measured-error source or SyncE
dependency is introduced.

`DpllConfig.ReplaceHoldoverModel` provides a local Go replacement seam, not an
external configuration or control-plane API. Replacement preserves the current
episode's elapsed origin, acquired validity and quality latch, then requests a
new evaluation. It cannot bypass disabled, invalid or non-applicable hardware
configuration. Nil model references are rejected.

The DPLL serializes evaluations without holding its state mutex while calling
the model. Before publishing, it checks both model revision and episode
generation. In-flight results **and errors** from an old model or episode are
discarded after replacement, recovery, stop or reset. A current active model
error fails closed to free run with a diagnostic. A model is never evaluated to
make holdover decisions while the hardware is actually locked.

Resolved holdover configuration is fixed for each DPLL instance; configuration
reapplication constructs a new instance rather than mutating an active model's
thresholds or duration.

Qualified reacquisition restores class 6, stops the old episode and clears its
elapsed/error bookkeeping. A subsequent source loss starts at elapsed zero.
PTP/PPS-led T-BC, BC/OC timers and hardware-slaved `FlagOnlyPhaseStatus` paths
retain their existing behavior.

The public behavioral reference is the [T-GM specification](https://github.com/ptp-spec-driven/clock-requirements/blob/main/tgm/spec.md),
especially sections 6.2, 6.4, 6.8, 8 and 12.5.
