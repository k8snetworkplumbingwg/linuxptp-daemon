# Shared holdover estimation

## Observation and interpretation boundary

DPLL now publishes immutable raw hardware state, measured/nil offset and explicit
nonnegative modeled error independently. Its common episode worker owns elapsed
time, model evaluation, revision/generation fencing and cancellation. Receiving
clocks interpret accepted observations before aggregation; manager metrics/logs
use the accepted clock reporting view and reject retired producers/episodes.

This architectural boundary deliberately retains old source-driven policies:
GNSS error strictly above I ends holdover when frequency is not traceable; a
separate F crossing does not end legacy GNSS holdover. Nil measurements skip
offset thresholds. Timeout remains a hard exit, including at equality. Old
class mapping stays HOLDOVER7 and traceable out-of-spec FREERUN140, otherwise248.
The receiving clock still reports the legacy estimate/fault offset while the
producer retains the distinct raw measurement. PTP-leading TBC keeps135/165,
its existing latch, hardware fault handling and hard timeout. Runtime model
replacement is GNSS T-GM-only and retains episode origin/cumulative facts.
No new T-GM quality policy, Unknown override or traceability matrix is enabled.

GM PMC settings reconciliation runs outside the clock manager event lock, with
one cancellable worker per owner, latest-request coalescing and retry backoff.
Removal, replacement and shutdown cancel first and join outside the event lock.
This transport/lifecycle refactor retains the existing class, measured-offset
accuracy and outgoing traceability policy; it does not enable new T-GM states.

`dpll.HoldoverModel.Estimate(time.Duration) (int64, error)` estimates a
nonnegative error in nanoseconds independently of clock type. The default
rounded linear estimator uses the resolved maximum offset divided by timeout
in seconds, multiplied by elapsed seconds. HardwareConfig and legacy profile
limits reach the same DPLL constructor; no hardware selector or model is added.

Both existing GNSS-leading T-GM and PTP-leading T-BC holdover workers call this
interface. A validated `WithHoldoverModel` constructor option supplies a model
per instance. Models must be immutable, bounded, concurrency-safe and perform
no I/O. No runtime replacement API is provided at this boundary. Nil/typed-nil
injection is rejected, and invalid estimates fail an active attempt closed.
Zero maximum offset with a positive timeout retains zero drift; no positive
default is silently inserted. The public positive linear constructor rejects
zero limits and negative elapsed/unrepresentable results return errors.

Estimation does not change the existing clock policies: GNSS without frequency
traceability still exits to free run at its in-spec error crossing. Legacy
PTP-leading holdover may continue out of spec until its hard timeout, and
existing offset reporting, flags, class mappings and source qualification are
unchanged. There is no new T-GM class140 behavior in this estimator extraction.

Unit tests exercise the rounded default, limits/errors, constructor injection
and nonlinear models through both actual legacy holdover workers. They do not
prove physical hardware drift, deployed timing or PMC wire interoperability.
