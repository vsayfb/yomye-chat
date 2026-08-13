# Chat Local and Staging Smoke Test

The Chat smoke runner validates the deployed private direct-message path using
real Core-owned identities and real Chat HTTP/WebSocket interfaces. It never
creates Core users, signs Core JWTs, queries Core PostgreSQL, writes credentials
to disk, or adds a test authentication path to Chat.

## Credential manifest contract

Core must write one JSON value to stdout. Version 1 is the minimum supported
contract:

```json
{
  "version": 1,
  "run_id": "<deployment-sha>",
  "expires_at": "<RFC3339 timestamp>",
  "users": [
    {
      "id": "<UUID>",
      "name": "<display name>",
      "access_token": "<short-lived Core access JWT>"
    }
  ]
}
```

Requirements for Core's future `smoke credentials` command:

- Return exactly 15 users with distinct, non-nil UUIDs and distinct access
  tokens.
- Set `version` to `1` and echo the requested run ID exactly.
- Set `expires_at` to the earliest time at which the manifest or any included
  access token should no longer be used.
- Issue normal Core HS256 access JWTs with `iss=yevmiye-core`,
  `token_use=access`, matching UUID subjects, and valid expiries.
- Ensure the selected cohort can establish social direct conversations. Chat
  enforces `allow_social_direct_messages` through Core's existing `GetUsers`
  contract; the smoke runner does not bypass this preference.
- Print only the JSON manifest to stdout. Operational diagnostics belong on
  stderr and must not contain tokens.

Chat reads this manifest only from stdin, caps its size, rejects unknown fields,
requires an unexpired matching run ID and exactly 15 distinct users, and keeps
the decoded tokens only in process memory. It checks the unverified JWT envelope
and claims for early contract diagnostics; the deployed Chat authentication
middleware remains authoritative because only it verifies the Core signature.
Credential file and command-line token inputs are intentionally unsupported.

## Build

Build the local maintenance command:

```bash
make build-smoke
```

This creates `bin/chat`, whose command shape is `chat smoke run`.

Build the staging Linux binary:

```bash
make build-smoke-linux
```

## Local execution

Start Core and Chat with their real local infrastructure. Complete Core's smoke
run first, then pass that exact run ID to Chat's one-command consumer:

```bash
make smoke-local CORE_SMOKE_RUN_ID=local-002
```

Optional Chat overrides:

```bash
make smoke-local \
  CORE_SMOKE_RUN_ID=local-002 \
  CHAT_SMOKE_TARGET=http://127.0.0.1:8081 \
  CHAT_SMOKE_TIMEOUT=15m
```

`CORE_SMOKE_RUN_ID` is deliberately required. Chat does not generate a latest
or timestamped run ID because that could bind the test to a different cohort
than the one Core completed. The Make target does not run `core smoke run`; it
only requests the existing run's credential manifest and streams it directly
from `core_svc` into the `chat-service` container's stdin.

Do not enable shell tracing (`set -x`) around the credential pipe. Do not use
`tee`, redirection, temporary files, or CI output capture on the manifest.

Core's `smoke credentials` command is not implemented in this repository and
does not exist yet. It remains an external dependency; `make smoke-local` will
fail until Core implements the version 1 producer contract above. Chat does not
query Core PostgreSQL or invent a fallback.

## Scenarios

The runner uses the first three manifest users and:

1. Reuses or creates the unique direct conversation for users A and B.
2. Sends deterministic Turkish messages in both directions.
3. Verifies real-time delivery for messages newly created in that run.
4. Retrieves durable history as user B and verifies conversation, sender, and
   recipient identities plus ordering.
5. Retries both stable `client_message_id` values and verifies the durable IDs
   do not change and history contains no duplicates.
6. Reads history through the composite `(sent_at, _id)` pagination cursor.
7. Verifies user C receives `403` through HTTP and cannot open the conversation
   WebSocket, preventing both reads and message mutation.
8. Reconnects through a new one-time ticket and verifies history remains
   available.

The first-contact WebSocket authorization exercises Chat's real Core `GetUsers`
summary lookup and privacy preference. Chat has no unread/read state, delivery
receipts, or read-receipt endpoint, so the runner does not invent those checks.

On a repeated run ID, the stable message IDs replay rather than rebroadcast.
The runner validates persistence and idempotency and reports `realtime=0` for
already-existing messages. This is expected and prevents uncontrolled duplicate
messages. A partially completed prior run creates only its missing deterministic
message.

## Notifications

Chat publishes best-effort `new_message` SQS events only when no relevant
recipient WebSocket accepts live delivery. This scenario keeps both users online
to verify real-time delivery, so it does not intentionally trigger Lambda.

Chat currently has no configured authenticated Core notification API contract
through which the runner can verify a stored notification. Do not claim FCM
display or infer it from `pushed_at`. Add an optional cross-service assertion
only after Core documents its notification endpoint and the staging workflow
provides its origin. Lambda must continue deduplicating by durable `message_id`.

## Staging deployment

The Chat staging workflow builds and uploads both the API and maintenance
binaries. The smoke step runs through SSM on the EC2 host, waits for `/ready`,
downloads matching binaries from the private deployment bucket, runs Core smoke
first, and pipes credentials directly into Chat. A shell trap removes both
binaries.

The workflow input `run_smoke` is temporarily `false` by default because Core's
maintenance command is not available yet. Before enabling it, Core must upload
its Linux maintenance binary to the private deployment bucket. Configure the
staging variable `CORE_SMOKE_S3_KEY` with the artifact key for the Core version
currently deployed on that host; Core and Chat repository commit SHAs are not
assumed to be equal. Also define `CHAT_SMOKE_TARGET` as the non-production Chat
HTTP origin reachable from the EC2 host. Once `run_smoke=true`, any Core or Chat
smoke failure fails the deployment job. The workflow does not access a private
database from GitHub Actions and does not perform automatic rollback.

## Safe output and failures

Success prints only:

```text
chat smoke passed: run=<id> users=3 conversations=1 messages=2 realtime=<0..2>
authorization=passed
```

Failures name the scenario and may contain safe HTTP status codes, paths, or
durable entity identifiers. They never include access JWTs, one-time tickets,
Authorization headers, WebSocket query strings, or complete private message
bodies. Presigned attachment URLs are not used by this smoke scenario.

Production is explicitly unsupported. The runner accepts only
`--environment local` or `--environment staging`; `production` is rejected
before stdin is read.
