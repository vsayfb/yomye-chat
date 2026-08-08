# AGENTS.md

## Purpose

This repository owns Yövmiye's private conversation service. Treat privacy,
authorization, and durable message state as higher priority than convenience or
feature breadth. Users may exchange private text and media here; never weaken a
security invariant merely to make a client flow work.

Read this file before changing the service. Then inspect the relevant code and
tests: this document records intent and trade-offs, but code may have evolved.

## Collaboration style

The maintainer communicates informally and expects a direct technical partner,
not ceremony. Lead with the outcome or concrete diagnosis. Explain trade-offs
plainly, especially when privacy, latency, or cross-service ownership is
involved. Do not agree reflexively: use code/log evidence and call out unsafe
assumptions.

When the maintainer says an idea is still being discussed or says not to
implement yet, inspect and design only. Once implementation is approved, work
autonomously through verification. Ask only about choices that materially alter
the product or security model and cannot be inferred safely.

Security outranks functionality for private files and conversations. Be honest
about what the system does not guarantee. Do not market metadata checks as
content scanning, private storage as end-to-end encryption, or a successful PUT
as a safe file.

Commits and pushes are separate explicit actions. Implementing or verifying a
change does not authorize either one. When asked to inform the client engineer,
provide a copy-ready contract with endpoints, payloads, state transitions,
required headers, stable errors, retry behavior, and security cautions.

## Product boundaries

There are two deliberately separate chat domains:

1. **Applications/listing conversations** (`internal/application`, `/ws`)
   are tied to a gig/listing. They have their own inbox and status workflow.
2. **Social direct conversations** (`internal/conversation`, `/ws/direct`)
   are casual conversations unrelated to a gig.

Do not unify these inboxes, collections, WebSocket protocols, or HTTP list
endpoints unless the product explicitly reverses this decision. The Flutter
client queries them separately.

The general `Conversation`/`Member` model was chosen so group conversations can
be added later. Only two-person `TypeDirect` conversations are supported now.
Do not prematurely implement groups, but avoid redesigning direct chat into a
shape that makes groups impossible.

The service intentionally uses Gorilla WebSocket and its own hub. Do not add
Socket.IO merely for reconnection or rooms; the client owns token refresh and
best-effort reconnect behavior.

## Ownership and external dependencies

- **Chat** owns applications, social conversations, messages, attachment
  records, participant authorization, and private R2 access.
- **Core** owns users, gigs/listings, authentication/token issuance, privacy
  preferences, and the authoritative user/gig gRPC APIs.
- **Flutter** owns refreshing the short-lived access token and reconnecting a
  socket with the new token.
- **R2** stores private chat attachment bytes. It does not make application
  authorization decisions and does not validate that bytes are genuinely an
  image/video.
- **SQS** carries offline notification events. Notifications must never expose
  raw custom-emoji tokens or private attachment URLs.
- **MongoDB** is the durable store. Attachment message commits require MongoDB
  transactions, hence a replica set or mongos is mandatory.

Core's public media uses Cloudinary; do not move public-media ownership into
Chat. Likewise, do not make Core authorize private attachment downloads. Chat
must check current conversation participation itself.

## Authentication and WebSocket lifetime

Core access tokens are signed JWTs. Chat accepts a token only when all of the
following are true: `alg == HS256`, `iss == yevmiye-core`, `token_use == access`,
the signature validates with Core's configured access-token secret, `exp` is
present and valid, and `sub` is a non-nil valid UUID. Chat does not refresh
tokens.

Core refresh tokens are deliberately outside this JWT contract: they are opaque
64-character random hexadecimal values, and Core stores only their SHA-256
hashes. A genuine Core refresh token therefore cannot be mistaken for a Chat
access token because it is not JWT-shaped or signed.

WebSocket connections currently receive the access token as the `token` query
parameter. After verification, Chat derives the connection deadline from the
JWT `exp` claim. At expiry it sends close code `4001` with reason
`access_token_expired`, then closes the connection. The client should refresh
and reconnect; never keep a socket alive beyond its verified token expiry.

Treat query-string JWTs as bearer credentials. Never log full URLs/query
strings. A future authentication transport may be safer, but Flutter Web
compatibility must be evaluated before changing the contract.

Current WebSocket origin checking accepts every origin in
`internal/ws/handler.go`. This is a development compromise, not a production
security guarantee. Tighten it before exposing a browser deployment, while
accounting for native Flutter clients that may not send browser-style Origin.

## Application/listing conversation rules

Connection modes for `/ws`:

- First contact: `token`, `targetID`, and `gigID`.
- Reconnect: `token` and existing Mongo `threadID`.

The first text message lazily creates a pending application. The initiator may
not send another message while it is pending. The target must approve it;
accepted applications permit both participants to chat. Rejected or archived
applications reject further messages.

Only the target may approve or reject. Every application read, history fetch,
attachment operation, and reconnect must verify that the caller is one of the
two participants.

## Social direct-message rules

Connection modes for `/ws/direct`:

- First contact: `token` and `targetID`.
- Reconnect: `token` and existing Mongo `conversationID`.

Self-conversations are forbidden. A canonical sorted `direct_key` and a unique
Mongo index ensure one direct conversation per pair.

Core exposes `User.allow_social_direct_messages` as protobuf field 4. It
defaults to false. Chat checks it before creating a new social conversation and
fails closed if the field/user is missing or malformed. Existing conversations
continue to work without rechecking the creation preference; changing that
would be a separate blocking/leave-conversation product feature.

At present, `AllowsSocialDirectMessages` intentionally uses Core's `GetUsers`
summary RPC with exactly one requested ID and verifies the returned ID. Core's
single-user `GetUser` path previously omitted field 4. Do not switch back until
that contract is demonstrably fixed and generated protobuf packages are
consistent.

The first message in any new direct conversation is plain text. Attachments
require an already-existing `conversationID`.

## WebSocket message contract

Client message:

```json
{
  "client_message_id": "client-generated-id",
  "content": "text or canonical emoji tokens",
  "attachment_ids": ["mongoObjectId"]
}
```

After persistence, Chat sends the sender a `message.accepted` event containing
the client ID, durable message ID, conversation/thread ID, and timestamp. This
acknowledgement is the client's idempotency/reconciliation signal; it is not a
database uniqueness guarantee for `client_message_id`.

Logic and validation failures are returned as structured WebSocket errors:

```json
{
  "type": "error",
  "client_message_id": "...",
  "code": "stable_machine_code",
  "message": "User-friendly message."
}
```

Preserve stable error codes and human-readable messages. Invalid content must
not be stored or broadcast.

WebSocket client frames are capped at 64 KiB. Message content is capped at
4,000 logical characters; valid mascot tokens count as one logical character.
Over-limit content returns `message_too_long`.

The in-memory hub supports multiple devices/tabs. Delivery is routed by user,
domain channel, and direct counterpart to prevent a direct message appearing
on a socket opened for another person. The hub is process-local; horizontal
scaling will require a cross-instance fan-out mechanism or sticky routing.

Each socket has a bounded outbound queue, write deadlines, and ping/pong
liveness. Producers must enqueue through `hub.Client.Send` rather than writing
to the Gorilla connection directly. During shutdown, stop HTTP acceptance,
drain/close the hub, wait for WebSocket handlers, then disconnect MongoDB.

## Mascot emoji contract

Message content remains a string. Only these exact lowercase tokens are valid:

- `:yovmiye_determined:`
- `:yovmiye_excited:`
- `:yovmiye_focused:`
- `:yovmiye_happy:`
- `:yovmiye_neutral:`
- `:yovmiye_skeptical:`
- `:yovmiye_surprised:`
- `:yovmiye_thinking:`

Reject unsupported or malformed content in the reserved `:yovmiye_*:`
namespace with `invalid_emoji`. Emoji-only messages are valid. Store and return
canonical tokens unchanged. Never accept emoji URLs, HTML, Base64, or media
bytes as message content.

For logical message length, a valid mascot token counts as one character.
System-notification previews replace tokens with `[Yövmiye emojisi]`; attachment
only previews use `[Ek]`.

## Private attachment architecture

Security is more important than attachment convenience. The private bucket is
not a substitute for application authorization.

Supported scopes and generated keys:

- Direct: `chat/conversations/{conversationID}/{random-id}.{ext}`
- Application: `chat/applications/{threadID}/{random-id}.{ext}`

Never accept an object key from the client. Generate it with `NewObjectKey` and
keep `object_key` out of JSON responses. All R2 operations validate the
configured `chat/` namespace.

Lifecycle:

```text
PENDING_UPLOAD -> READY -> ATTACHED -> DELETED
                       \-> DELETING -> DELETED
```

`DELETING` is an internal durable cleanup/claim state. Pending uploads expire
after 1 hour; confirmed but unattached ready objects expire after 3 hours.
Cleanup deletes R2 first, then tombstones the database record, and persists
retry state on failure.

Limits:

- Images: JPEG, PNG, WebP, GIF; maximum 2 MiB.
- Videos: MP4, QuickTime/MOV, WebM; maximum 75 MiB.
- Client-declared video duration must be 1..60,000 ms.
- Maximum 2 unique attachments per message.

The declared duration is only best effort. R2 `HEAD` cannot inspect real video
duration.

Upload flow:

1. Authenticate and verify current participation in an existing scope.
2. Validate declared filename, MIME, size, and duration.
3. Generate the object key and create `PENDING_UPLOAD` before presigning.
4. Return a short-lived presigned PUT URL, method, required headers, and
   attachment ID. Never return the key.
5. Client uploads bytes directly to R2; Chat/Core receive no upload bytes.
6. Client confirms by attachment ID.
7. Recheck uploader and current participation, then use authoritative R2 HEAD
   metadata to verify exact size and normalized MIME.
8. Idempotently move the record to `READY`.

Presigned PUT details are security-critical:

- Do not sign or require `Content-Length`; browsers control it.
- Sign and return `Content-Type`.
- Sign and return `If-None-Match: *` so the generated staging key is write-once
  and the URL cannot overwrite a confirmed object.
- R2 browser CORS must allow `PUT`, `Content-Type`, and `If-None-Match`.
- Treat every presigned URL as a temporary bearer credential. Never log,
  persist, cache, analytics-track, or expose it beyond the authorized client.

Only `READY` attachments owned by the sender and belonging to the exact message
scope can be attached. Message creation and all `READY -> ATTACHED` transitions
run in one Mongo transaction. Never replace that with best-effort sequential
writes. Attachments require an existing thread/conversation, which preserves
the product rule that the first message is text-only.

Downloads resolve by attachment ID, load the record, recheck current
participation, and only then issue a short-lived presigned GET. URLs are never
stored as attachment URLs. Downloads currently force attachment disposition;
there is intentionally no inline-view endpoint.

## Attachment threat-model limitations

Current verification proves key namespace, declared/stored MIME metadata, and
exact object size. It does **not** prove magic bytes, decode safety, malware
absence, codec safety, or real video duration. A malicious file can lie through
metadata. Forced download reduces browser rendering exposure but does not make
the file safe.

Do not add inline rendering or broaden file types until there is a shared
app-wide quarantine/scanning pipeline. The preferred future flow is
`PENDING_UPLOAD -> QUARANTINED/PROCESSING -> READY` with sandboxed signature
inspection, bounded image decode/re-encode, `ffprobe` video validation, and
malware scanning. This belongs in shared media infrastructure because Core's
Cloudinary media has the same general content-validation concern. Each service
must still retain its own authorization and lifecycle ownership.

## HTTP API

Authenticated REST routes use bearer JWT middleware:

- `GET /applications/me`
- `GET /applications/{threadID}`
- `GET /applications/{threadID}/messages?before=<RFC3339Nano>&before_id=<ObjectID>&limit=<1..100>`
- `POST /applications/{threadID}/approve`
- `POST /applications/{threadID}/reject`
- `POST /applications/{threadID}/attachments/uploads`
- `GET /conversations/me`
- `GET /conversations/{conversationID}`
- `GET /conversations/{conversationID}/messages?before=<RFC3339Nano>&before_id=<ObjectID>&limit=<1..100>`
- `POST /conversations/{conversationID}/attachments/uploads`
- `POST /attachments/{attachmentID}/confirm`
- `GET /attachments/{attachmentID}/download`

Application and social inbox/history endpoints remain separate.

Message-history pagination is ordered by `(sent_at DESC, _id DESC)`. The first
page omits both cursor parameters. Every subsequent page must provide both
`before` and `before_id`, copied from the last message on the previous page. A
timestamp-only cursor is intentionally rejected because messages can share the
same MongoDB timestamp and would otherwise be skipped at page boundaries.

## Production-readiness decisions

The following decisions correspond to the August 2026 production-readiness
review. Preserve these statuses unless the maintainer explicitly revisits them.

- **JWT access-token contract — implemented.** Chat accepts only Core access
  JWTs that use HS256, declare `iss=yevmiye-core` and `token_use=access`, validate
  with Core's configured access-token secret, contain a valid non-expired `exp`,
  and use a non-nil UUID in `sub`. Core refresh tokens remain opaque 64-character
  hexadecimal values whose SHA-256 hashes are stored by Core, so they cannot be
  parsed as access JWTs. Keep Chat's verifier aligned with this Core contract.
- **Durable notification outbox — accepted open decision.** Message persistence
  remains authoritative and SQS notification publishing remains best effort
  after the message is durable. An SQS failure must not make the client retry a
  successfully stored message. This means an offline notification can be lost.
  That reliability trade-off is accepted for now; revisit an outbox/durable
  retry design before stronger production notification guarantees are required.
- **Durable `client_message_id` idempotency — deferred.** The field remains a
  correlation/acknowledgement identifier only. Database uniqueness and replay
  handling will be designed later.
- **WebSocket origin and query-string JWT transport — deferred.** The current
  browser-origin policy and `?token=` transport remain unchanged until the
  staging proxy/browser/native-client topology is known. Never log the query
  string in the meantime.
- **Mongo indexes — implemented.** Application inbox queries have separate
  `(initiator_id, last_message_at DESC)` and `(target_id, last_message_at DESC)`
  indexes. Application message history uses
  `(thread_id, sent_at DESC, _id DESC)`. Direct message history uses
  `(conversation_id, sent_at DESC, _id DESC)`. New names are used for changed
  message index specs so existing deployments do not fail startup on an index
  name/spec conflict.
- **Stable message pagination — implemented.** History uses the composite
  `(sent_at, _id)` cursor described in the HTTP API section. Both cursor values
  are required together.
- **Application approve/reject race — implemented.** Pending status transitions
  are compare-and-set Mongo updates matching `_id`, `target_id`, and
  `status=pending` atomically. A failed compare-and-set may be followed by a
  read only to preserve the existing not-found/forbidden/conflict HTTP mapping;
  authorization and state transition correctness must not depend on that read.
- **Core gRPC deadlines — implemented.** Chat applies a 3-second timeout to
  each Core `GetUser`, `GetUsers`, and `GetGig` RPC while preserving any earlier
  caller deadline. `grpc.NewClient` remains lazy and is not treated as a Core
  health check; request-time calls are bounded rather than allowed to hang on
  transport/connectivity behavior.
- **Core gRPC transport security — deferred.** Chat still uses plaintext gRPC
  transport to Core. This is acceptable for staging only when the traffic stays
  on the intended private network and security-group boundary. Revisit TLS/mTLS
  when the deployment topology is finalized; do not silently expose this hop to
  an untrusted network.
- **HTTP edge hardening — implemented.** The HTTP server uses a 5-second
  `ReadHeaderTimeout`, a 60-second keep-alive `IdleTimeout`, and a 64 KiB maximum
  header size. Request handlers are wrapped in structured panic recovery that
  does not log panic values and does not overwrite a response that already
  started. Do not set global `ReadTimeout`/`WriteTimeout` merely to satisfy a
  generic checklist: long-lived WebSocket upgrades have their own read/write
  deadlines and liveness handling. Rate limiting is a separate deferred item.
- **Readiness endpoint — deferred.** `/health` remains a simple liveness check.
  A separate readiness contract for MongoDB/Core/other required dependencies
  will be designed later instead of making liveness depend on downstream health.
- **Staging logging — implemented.** `staging` and `production` use structured
  JSON logs at INFO level; `development` uses text logs at DEBUG level. Keep the
  environment string aligned with `config.EnvironmentStaging`; do not reintroduce
  the obsolete `stage` spelling.
- **OpenTelemetry hardening — implemented.** OTLP remains the single traces and
  metrics export path. Telemetry initialization must establish a gRPC connection
  to the configured collector within 5 seconds before it is considered ready;
  startup remains non-blocking and retries every 30 seconds until shutdown if
  the collector is unavailable. Root traces use parent-based ratio sampling via
  `OTEL_TRACE_SAMPLE_RATIO`: development defaults to `1.0`, staging to `0.25`,
  and explicit values must be between 0 and 1. The unused
  `METRICS_SERVER_PORT` setting was removed; do not add a second metrics server
  unless the observability architecture intentionally changes.
- **Rate limiting and caching — deferred until stabilization.** Do not add
  per-user/IP WebSocket, message, attachment-authorization, confirmation, or
  download rate limits yet, and do not introduce caching merely as premature
  optimization. Revisit both after the service behavior and deployment are in a
  good/stable stage, with abuse limits designed around actual traffic.
- **Staging CI/CD hardening — deferred.** Test/lint/race gates, deployment
  atomicity, health verification, and rollback behavior will be reviewed later.
- **Production configuration path — deferred.** `loadGCP` remains unimplemented
  and production mode is still a blocker. Do not claim the service supports the
  production environment until this path is intentionally designed and tested.

## Configuration and deployment

Configuration modes currently are:

- `development`: environment variables via `config/env.go`.
- `staging`: AWS default credential chain plus SSM parameters under the
  hard-coded `/yevmiye/staging/` prefix. OTel collector address remains an
  environment setting and trace sampling defaults to 25% unless
  `OTEL_TRACE_SAMPLE_RATIO` overrides it.
- `production`: `loadGCP`, which currently returns `nil, nil` and is not
  production-ready. Treat this as a blocker, not a harmless placeholder.

Staging SSM parameters:

- `mongo-db-uri`
- `mongo-db-name`
- `sqs-notification-events-queue-url`
- `jwt-secret`
- `r2-account-id`
- `r2-access-key-id`
- `r2-secret-access-key`
- `r2-bucket`

Sensitive values should be `SecureString`; the runtime role needs least-
privilege `ssm:GetParameters`, relevant `kms:Decrypt`, and `sqs:SendMessage`.
R2 uses separate Cloudflare S3-compatible credentials. Chat does not use
`R2_TOKEN_VALUE` or `R2_ENDPOINT`; the endpoint is derived from the account ID.

The R2 access key and secret are master credentials for their granted scope,
not client configuration. Keep them server-side, restrict them to the private
chat bucket and required operations, and rotate on any exposure.

Historical commits contained live-looking R2 credentials in `.env.example`.
The current R2 examples must remain placeholders; never restore or quote old
values. Confirm those credentials have been revoked before staging, and
remember that removing them from the current file does not remove Git history.
The committed JWT value is deliberately a development-only shared secret for
local services. It must never be reused in staging or production.

Staging MongoDB must be Atlas/replica-set/mongos compatible. Standalone Mongo
produces `Transaction numbers are only allowed on a replica set member or
mongos` and cannot safely support attachment message commits.

Core gRPC currently uses plaintext transport. It is acceptable only on a
properly isolated private network/security group; otherwise add TLS/mTLS.
External HTTP/WSS TLS is expected to terminate at trusted infrastructure.

## Known operational/security debt

Before treating the service as internet-production-ready, explicitly review:

- WebSocket `CheckOrigin` currently allows all origins.
- Per-user/IP rate limiting and caching are explicitly deferred until the
  service is otherwise stable; socket connects/messages and attachment grant
  endpoints therefore still have no abuse-rate controls.
- `/health` is liveness only; readiness is explicitly deferred.
- Core gRPC transport remains plaintext pending the deferred TLS/mTLS decision.
- Staging CI/CD quality gates and rollback semantics are explicitly deferred.
- The hub is in-memory and process-local.
- Query-string JWT transport can leak through careless proxy/access logging.
- Attachment content is not signature-validated or malware-scanned.
- Production GCP configuration is unimplemented.
- Notification delivery remains best effort by explicit open decision; see the
  production-readiness decisions above before changing that contract.
- `client_message_id` durable idempotency is explicitly deferred; client retries
  can still create duplicates.

Do not silently expand scope while fixing these. State the threat/trade-off,
make the smallest safe change, preserve compatibility where possible, and add
tests around every security boundary.

## Working rules for future agents

- Inspect `git status` first. Preserve user-owned and unrelated changes.
- Never print secrets, JWTs, presigned URLs, full environment dumps, or
  credential-bearing Docker inspection output.
- Do not commit or push unless the user explicitly asks.
- Do not change API/WebSocket contracts without describing the client impact.
- Authorization checks must occur before existence-sensitive/private results
  are returned where practical; fail closed on malformed external contracts.
- Recheck participation for attachment confirmation and every download grant.
- Never trust client object keys, filenames, MIME types, sizes, durations,
  attachment ownership, conversation IDs, or user IDs.
- Keep external calls out of Mongo transaction callbacks because the driver may
  retry them.
- Invalid messages/attachments must never be stored or broadcast.
- Preserve structured, user-friendly WebSocket errors.
- Prefer primary-source documentation when behavior depends on R2, AWS, Mongo,
  browser WebSocket, or another evolving external system.

## Verification

For normal Go changes, run at least:

```bash
go test ./...
go vet ./...
git diff --check
```

For concurrency, hub, WebSocket, transaction, or attachment changes, run:

```bash
go test -race ./...
```

If `go` is not on PATH in the agent environment, use
`/usr/local/go/bin/go`. If the default Go build cache is read-only, set a
task-specific cache under `/tmp`, for example:

```bash
GOCACHE=/tmp/chat-service-go-cache /usr/local/go/bin/go test -race ./...
```

Do not claim transaction behavior is integration-tested unless it was exercised
against an actual replica set/mongos. Unit tests alone cannot prove deployment
topology.
