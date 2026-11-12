# Clock event processing and reporting

`Clock.AddEvent` returns `EventResult`: the synchronization state and a reporting
decision from the same processing call. A nil `Report` passes the original event
through. A non-nil `Report` replaces the event used for logs and event metrics,
including for non-DPLL sources. `SuppressReport` takes precedence over either
form and stops the manager's subsequent reporting, state notifications, metrics
and statistics for that input. Processing has already happened; suppression does
not roll back clock state.

GM and T-BC prepare separate policy and reporting views in one observation
acceptance step, before aggregation. Storage copies the policy sample's fields;
subsequent clock processing changes composite state rather than those fields, so
accepted reports need no later storage search or identity check. An exact
duplicate skips processing but retains the previously accepted reporting values;
that path still checks the retained sample, while stale or reset observations
without a matching retained sample suppress reporting. Raw observations and
stored policy offsets remain separate from the reporting copy. BC and OC use
default reporting. HA preserves a member's processing result and overlays its
profile status; source-selection and unknown-member inputs retain default
reporting and the composite synchronization state.

This changes the public Go `Clock.AddEvent` return type from `SyncState` to
`EventResult`. Implementations and adapters must return the new type; callers
needing the old value explicitly can use `result.SyncState`. Embedded state fields
remain accessible directly. The old separate `DPLLReport` query is replaced by
the result of processing, so callers must not process an event a second time to
obtain its report. This is a Go API change, not a physical protocol, metric label,
IPC or protobuf schema change.
