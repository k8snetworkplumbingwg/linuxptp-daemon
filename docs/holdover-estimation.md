# Shared holdover estimation

`dpll.HoldoverModel.Estimate(time.Duration) (int64, error)` estimates a
nonnegative error in nanoseconds independently of clock type. The default
rounded linear estimator uses the resolved maximum offset divided by timeout
in seconds, multiplied by elapsed seconds. HardwareConfig and legacy profile
limits reach the same DPLL constructor; no hardware selector or model is added.

Both GNSS-leading T-GM and PTP-leading T-BC use the same modeled holdover worker and
admission policy when phase offsets are supported. They call this
interface. A validated `WithHoldoverModel` constructor option supplies a model
per instance after configuration and constructor options are resolved. The model
is fixed for the instance lifetime and reused with each episode's elapsed time.
Models must be immutable, bounded, concurrency-safe and perform
no I/O. Nil/typed-nil
injection is rejected, and invalid estimates fail an active attempt closed.
Zero maximum offset with a positive timeout retains zero drift; no positive
default is silently inserted. The public positive linear constructor rejects
zero limits and negative elapsed/unrepresentable results return errors.

In the modeled path, error strictly greater than `LocalMaxHoldoverOffSet` takes
terminal FREERUN precedence. Otherwise error greater than `MaxInSpecOffset` or
elapsed time greater than `LocalHoldoverTimeout` latches out-of-spec HOLDOVER.
Equality does not cross a bound. Timeout ends in-spec operation, not the episode;
zero timeout disables entry before estimating. Zero terminal offset with positive
timeout retains zero drift and can stay out of spec indefinitely unless hardware
or error requires FREERUN. Qualified recovery resets the episode and its latch.
Hardware status remains authoritative. GM reports classes 7/140/248; BC reports
135/165/248, including timeout-only out-of-spec with a slow model. Hardware status
selection and `FlagNoPhaseOffset`/phase-only legacy fallback remain separate.

This arrival-based variant explicitly ASSUMES that received selected-input
phase measurements are current; receipt is not an independently verified driver
freshness guarantee. Raw netlink offsets remain separate from effective modeled
values. During admitted hardware HOLDOVER an offset-bearing selected-input update
immediately reports its signed value, classifies its absolute magnitude and
resets the modeled fallback deadline to one second after processing that arrival.
Zero and repeated values count. Missing phase attributes (the parser's MaxInt64
sentinel), unrelated inputs and device/status-only updates do not count. No
cached raw offset is promoted into a newly received measurement. Partial messages
without the existing clock/active-input/parent selector metadata are not accepted
by that selector; no new pin cache or invented selection contract is added.

At a fallback deadline the existing model estimates at TOTAL episode age and
rearms the one-second deadline. Arrivals never reset episode age or the separate
in-spec timeout. A timeout-quality deadline applies even when arrivals continually
postpone modeling. The out-of-spec latch clears only on qualified recovery. A
measurement does not alone establish recovery. Transitions into/out of hardware
holdover invalidate previous raw/reporting values as before. No-phase/phase-only
legacy paths remain separate and do not gain arrival-based modeling.

Logs identify `offset_source=measured` with
`reason=measurement_preempted_modeled`, signed `offset_ns`, total `elapsed` and
`modeled_fallback_after=1s`; modeled updates use `offset_source=modeled` and
`reason=fallback_deadline`. These labels describe received/assumed-current data,
not proof that hardware refreshed a cached sample. If timer and arrival contend,
mutex processing order applies: an arrival processed first moves the deadline;
an already processed terminal update remains terminal. No atomic priority over
unprocessed kernel messages is claimed.

Linux documents optional monitoring of inputs other than the active reference
in [DPLL phase offset monitor](https://docs.kernel.org/driver-api/dpll.html).
This does not establish generic sample freshness: the public `ice` driver can
return cached offsets or successful fallback zero, and a failed acquisition can
leave the cache unchanged. Accepting their arrivals here is an explicit policy
assumption, not a new driver-validity contract or a TTL for hardware freshness.

Unit tests exercise the rounded default, limits/errors, constructor injection
and nonlinear models through both roles' workers, strict shared policy, hardware
updates, recovery/retirement handoff, and real class consumers. They do not
prove physical hardware drift, deployed timing or PMC wire interoperability.
