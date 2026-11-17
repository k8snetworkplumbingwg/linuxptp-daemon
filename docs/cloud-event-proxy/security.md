# Secure CEPv2 event APIs

CEPv2 implements the CEPv1 mTLS and Kubernetes TokenReview contract natively.
There is no cloud-event-proxy repository or rest-api library dependency.
The separate event-consumer repository provides the subscriber and callback app.

The operator supplies `--auth-config=/etc/cloud-event-proxy/auth/config.json`.
The JSON options retain CEPv1 names:

```json
{
  "enableMTLS": true,
  "enableOAuth": true,
  "useServiceCA": true,
  "useOpenShiftOAuth": true,
  "requiredAudiences": ["https://kubernetes.default.svc"],
  "caCertPath": "/etc/cloud-event-proxy/ca-bundle/ca-bundle.crt",
  "serverCertPath": "/etc/cloud-event-proxy/server-certs/tls.crt",
  "serverKeyPath": "/etc/cloud-event-proxy/server-certs/tls.key",
  "clientCertPath": "/etc/cloud-event-proxy/client-certs/tls.crt",
  "clientKeyPath": "/etc/cloud-event-proxy/client-certs/tls.key",
  "serviceAccountName": "linuxptp-daemon",
  "serviceAccountToken": "/var/run/event-api-token/token",
  "allowedCallbackHosts": ["consumer-events-subscription-service.cloud-events.svc.cluster.local"],
  "tlsMinVersion": "VersionTLS12",
  "tlsCipherSuites": []
}
```

Both enabled mechanisms must pass on subscriptions, publishers and CurrentState
routes, including requests from IPv4 and IPv6 loopback. With hostNetwork,
loopback is a node boundary, not proof of a caller in the same pod. Source
addresses, Host values and forwarding headers never grant an authentication
exception. Only exact GET /api/ocloudNotifications/v2/health is exempt; other
methods on that path are unsupported. Endpoint authorization is not part of
this step.

CEPv2 ingests events through native daemon IPC and derives publisher discovery
from its resources. It does not expose legacy publisher registration, event
injection or debug APIs such as POST /publishers, /create/event or /dummy.
GET /publishers remains authenticated discovery. No internal HTTP self-call
migration is needed in CEPv2; the separate CEPv1 migration remains parked.
The native IPC permissions and privileged host boundary are unchanged.

The operator provisions a Service CA certificate for each node's publisher
Service. Its Pod admission webhook selects a direct mount of that node's Secret;
other nodes' serving keys are not projected into the Pod. The binary reads the
usual `serverCertPath` and `serverKeyPath` files. Subscription URLs use the node
Service, so later requests reach the same subscription store and event cache.
This limits mounted serving credentials; it does not change the privileged
workload boundary, existing RBAC, or shared outbound client identity.

Serving and client certificates are separate: Service CA certificates have
serverAuth usage, while outbound callbacks require a clientAuth certificate.
Startup rejects incomplete configured credentials. Callback requests use HTTPS,
verify the server hostname, present the client certificate and reread the token
file on each request. Before any credential-bearing request, including one from
a restored subscription or an HTTPS redirect, the callback hostname must match
`allowedCallbackHosts`. Exact names and leading `*.` subdomain patterns are
supported; the operator uses the standard consumer Service name rather than a
cluster-wide wildcard. Certificate/trust changes require a restart.

TokenReview successes are cached for 30 seconds with a 4096-entry bound. Cache
misses fail closed during API outages; cached successes can survive until their
TTL expires. The cache TTL is not capped by token expiry. OAuth naming denotes
bearer validation, not OAuth login, issuance, refresh or scope enforcement.

Upstream operator deployment enables security by default. The later 5.1 backport
must preserve explicit opt-in defaults; 5.2 defaults on. For an unsecured test
deployment set SECURED=false ENABLE_EVENT_AUTH=false in the operator. The binary
retains plaintext compatibility when no auth config or TLS flags are supplied.

## Local validation

```sh
CGO_ENABLED=0 go test -tags unittests ./pkg/auth ./pkg/cep ./cmd/cloud-event-proxy
```

Tests cover cumulative certificate/token checks, protected loopback and remote
requests, the exact GET health exception, unsupported internal APIs, missing
configuration, TokenReview cache and audience
behavior, and a TLS integration test covering subscription, initial notification,
CurrentState pull, later push, and rejection of anonymous requests. A separate
callback test verifies rotating bearer tokens. Cluster rollout,
subscribe/pull/push demonstration and performance measurements remain separate
release validation; these unit results are not cluster evidence.
