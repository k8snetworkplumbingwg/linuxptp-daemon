# Clock event processing and reporting

`Clock.AddEvent` returns `EventResult`: synchronization state and a generic
reporting decision from the same processing call. A nil `Report` passes the
original event through. A non-nil `Report` replaces the event used for logs and
event metrics. `SuppressReport` takes precedence and stops subsequent manager
reporting, state notifications, metrics and statistics. Processing has already
happened; suppression does not roll back clock state.

GM, T-BC, BC and OC preserve default reporting. HA preserves its member's result
and overlays profile status; source-selection and unknown-member events retain
the composite state and default reporting. The manager snapshots default log
text before processing, preserving its existing view of mutable event data;
metrics continue to consume the event after processing. A replacement supplies
its own log view after processing.

This changes the public Go `Clock.AddEvent` return type from `SyncState` to
`EventResult`. Implementations and adapters must return the new type; callers
needing the old value explicitly can use `result.SyncState`. Embedded state fields
remain accessible directly. This is a Go API change, not a physical protocol,
metric label, IPC or protobuf schema change.
