# Managed GM and T-BC PMC publication

GM and T-BC build announce policy on the serialized clock loop and submit
immutable requests to one owned PMC worker per clock. A slow settings GET, an
upstream parent/time/current dataset fetch, or either kind of SET leaves the
manager available to process unrelated events.

GM first obtains a baseline so inherited leap flags survive policy construction.
The complete settings retain the existing class, accuracy, UTC and traceability
rules. Unchanged observations are coalesced; subsequent observations request a
baseline refresh at most once every thirty seconds, including same-class UTC and
leap refreshes. T-BC retains its upstream selection and qualification checks and
process-unique announce token. Its local policy can intentionally publish only
external properties while holdover time properties are unavailable.

The shared engine applies controlled external properties, controlled settings,
then distinct local settings in order. Identical target/operation pairs are
deduplicated. The worker stops at the first failed operation and retries the
complete latest plan, with one-second minimum start spacing and exponential
one-to-thirty-second failure backoff. Bounded latest-state slots coalesce bursts.
Accepted policy and fully acknowledged publication are separate states.

This is eventual convergence across targets, not atomic rollback. Cancellation
cannot recall a SET already delivered to ptp4l. The latest plan repairs that
partial state. No hard shutdown time bound is promised.

`clock.ManagedWorkerOwner` is optional for other clock types. GM and T-BC expose
terminal, idempotent `StopManagedWorkers`, including before first use; `Reset`
invalidates outstanding work but permits subsequent recovery. The manager cancels
and detaches retired owners under its event lock, then joins outside that lock
before activating replacements. This ownership covers the new PMC workers, not
unrelated timers, daemon clients, or all HA member activity.

## Go client and direct-clock integration

The legacy `pmc.Client` interface remains available for other callers. A custom
GM client must additionally implement `pmc.GMSettingsClient`; a custom T-BC client
must implement `pmc.DownstreamClient`, including cancellable dataset GET and
external-properties SET. Constructors reject a missing context capability.
Transport methods must honor cancellation and return only after their transport
has stopped. Default transport owns and reaps its subprocess and removes its
private UDS directory before returning; SET acknowledgments are payload checked.

Direct GM/T-BC users install `SetManagedEventSink` before requesting PMC work.
The sink must use the supplied context to cancel a blocked queue send and route
the resulting `event.PMC` through the same serialized `AddEvent` loop. The manager
installs this sink automatically. The legacy loopback callback remains available
to other clocks; it is not an uncancellable fallback for managed results.

These are Go integration changes. Physical PMC commands, IPC and metric schemas
are unchanged. T-BC holdover enablement and the existing policy builders' input
selection remain unchanged.
