# Chat integration tests

The integration suite exercises Chat-owned behavior against real infrastructure.
It is intentionally separate from the package-local unit tests.

Run it with:

```bash
make test-integration
```

The runner starts a disposable MongoDB 8 single-node replica set with Docker
Compose, waits for a writable primary, then runs the Go integration package in a
`golang:1.26.4` container. The harness removes the containers and volumes
afterward, so the host does not need the project Go toolchain installed.

Set `KEEP_INTEGRATION_CONTAINERS=1` when debugging if you want the MongoDB
container to remain after the test command exits.

## Test boundary

- Chat repositories, services, HTTP handlers, WebSocket handlers, ticket store,
  and MongoDB behavior are real.
- Chat-owned persistence is never mocked in integration tests.
- External services may use protocol-level test doubles when the scenario needs
  them. For Core, that means a real TCP gRPC server implementing the protobuf
  contract rather than mocking Chat's internal client interface.
- Full deployed E2E tests may later use the real external services and staging
  infrastructure.

The first scenario covers direct-message retry idempotency using a real HTTP
server, real WebSocket connections, and real MongoDB persistence.
