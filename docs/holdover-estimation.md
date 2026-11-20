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

Raw netlink phase offsets are stored separately from modeled/reporting values.
The current scalar path provides no supported usable-current-measurement
contract during HOLDOVER or pre-LOCKED reacquisition. Such samples therefore
always use the model, even if a phase attribute is present, zero, nonzero or
recently received. A qualified measurement has precedence at the internal
selection seam, but its tests inject that contract; no actual HOLDOVER driver
capability is claimed by those tests. Locked pin updates still provide the
existing measured-offset qualification path. Transitions into/out of hardware
holdover invalidate the
previous raw/reporting sample, so an estimate cannot qualify locked recovery.

Linux documents optional monitoring of inputs other than the active reference
in [DPLL phase offset monitor](https://docs.kernel.org/driver-api/dpll.html).
This does not establish generic sample freshness: the public `ice` driver can
return cached offsets or successful fallback zero, and a failed acquisition can
leave the cache unchanged. Supporting those measurements in holdover requires
an observable driver-specific validity contract, not a receipt-time TTL or an
assumption that all holdover hardware lacks measurements.

Unit tests exercise the rounded default, limits/errors, constructor injection
and nonlinear models through both roles' workers, strict shared policy, hardware
updates, recovery/retirement handoff, and real class consumers. They do not
prove physical hardware drift, deployed timing or PMC wire interoperability.
