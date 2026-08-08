# Chat integration tests

The integration suite exercises Chat-owned behavior against real infrastructure
and real network protocols. It is intentionally separate from package-local unit
tests.

Run it with:

```bash
make test-integration
```

Run the same suite under Go's race detector with:

```bash
make test-integration-race
```

The runner starts a disposable MongoDB 8 single-node replica set with Docker
Compose, waits for a writable primary, then runs the integration package inside
a `golang:1.26.4` container. The host therefore needs Docker + Docker Compose,
not the repository Go toolchain.

Set `KEEP_INTEGRATION_CONTAINERS=1` while debugging if MongoDB should remain up
after the test command exits.

## Boundary rules

- Chat repositories, services, HTTP handlers, WebSocket handlers, ticket store,
  hub, MongoDB indexes, transactions, and persistence are real.
- Chat-owned persistence is never mocked in integration tests.
- External services may use protocol-level test doubles when Chat only owns the
  client side of the contract. Core tests use a real TCP gRPC server and the
  generated protobuf messages; they do not replace Chat's `grpcclient` with an
  in-process mock.
- SQS failure-isolation tests use the real AWS SDK SQS client against a local
  HTTP protocol endpoint so the production publisher path is exercised without
  depending on an AWS account.
- Full deployed E2E tests can later exercise the real staging Core/AWS/R2 stack.

## Critical scenarios covered

The suite covers the failure and security paths that would be expensive to learn
about only after deployment:

- direct-message reconnect/retry idempotency
- concurrent duplicate direct sends produce one durable message and one delivery
- concurrent duplicate accepted-application sends produce one durable message
- offline idempotent retries do not publish duplicate SQS notifications
- direct-message route isolation across multiple open chats for the same user
- application and direct WebSocket channels cannot cross-deliver
- application first-message creation and reconnect/retry idempotency
- concurrent first application creation produces one thread
- pending/rejected/archived application state guards prevent persistence
- concurrent application approve/reject has exactly one winner
- only the application target may approve/reject
- complete stable `(sent_at, _id)` pagination with equal timestamps
- partial/ambiguous pagination cursors are rejected
- private direct/application history and WebSocket resume authorization
- unauthenticated private HTTP routes are rejected
- required MongoDB indexes are actually created
- attachment READY -> ATTACHED transaction behavior and retry safety
- cross-conversation and wrong-uploader attachment reuse is rejected
- expired/unavailable attachments do not persist a message
- required `client_message_id`, malformed/empty message, and logical-length checks
- 64 KiB WebSocket frame limit prevents oversized-frame persistence
- Core access-token contract at the ticket endpoint, including algorithm,
  issuer, token-use, signature, expiration, UUID subject, and opaque refresh-token rejection
- browser Origin allowlisting, native no-Origin support, and development any-Origin behavior
- rejected browser Origin does not consume a one-time ticket
- one-time and expiring WebSocket ticket semantics
- legacy JWT-in-query WebSocket authentication rejection
- WebSocket closure when the underlying Core access token expires
- direct-message privacy preference enforcement through the real gRPC boundary
- missing/mismatched Core direct-user responses fail closed
- bounded Core RPC failure before WebSocket upgrade
- malformed/mismatched Core application user and missing gig responses fail closed
- Core failures do not mutate unrelated Chat persistence
- SQS notification failure does not roll back an acknowledged durable message
- `/ready` depends on MongoDB and draining state, not auxiliary dependencies
- hub shutdown closes active WebSockets and drains registered handlers

Every persistence-sensitive test checks MongoDB state directly. A successful HTTP
or WebSocket response alone is not considered evidence that a durability or
idempotency contract works.
