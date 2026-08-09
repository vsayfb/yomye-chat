# Cloudflare R2 CORS for Private Chat Attachments

This service uploads private chat attachments directly from an authorized
client to Cloudflare R2 using short-lived presigned `PUT` URLs. Browser clients
therefore require an R2 CORS rule. Native Flutter clients do not enforce browser
CORS, but they must still follow the same upload contract.

Private bucket CORS is not an authorization mechanism. Chat verifies the user,
conversation/application scope, target, and attachment lifecycle before issuing
a presigned URL. The URL itself is a temporary bearer credential and must never
be logged, persisted, cached, or sent to analytics.

## Recommended staging rule

Replace `https://staging.yovmiye.com` with the exact origin that serves the
Flutter Web application:

```json
[
  {
    "AllowedOrigins": [
      "https://staging.yovmiye.com"
    ],
    "AllowedMethods": [
      "PUT",
      "GET"
    ],
    "AllowedHeaders": [
      "Content-Type",
      "If-None-Match"
    ],
    "ExposeHeaders": [],
    "MaxAgeSeconds": 3600
  }
]
```

Configure this rule on the private chat attachment bucket in the Cloudflare R2
dashboard under the bucket's CORS policy.

## Local Flutter Web development

Add each local development origin with its exact port:

```json
[
  {
    "AllowedOrigins": [
      "https://staging.yovmiye.com",
      "http://localhost:3000"
    ],
    "AllowedMethods": [
      "PUT",
      "GET"
    ],
    "AllowedHeaders": [
      "Content-Type",
      "If-None-Match"
    ],
    "ExposeHeaders": [],
    "MaxAgeSeconds": 3600
  }
]
```

An origin contains only the scheme, hostname, and optional port. Do not include
a path or trailing slash. For example, use `http://localhost:3000`, not
`http://localhost:3000/attachments`.

## Required upload behavior

The client must use the method and every header returned by Chat's attachment
authorization response. A typical R2 request is:

```http
PUT <temporary-presigned-url>
Content-Type: image/jpeg
If-None-Match: *

<attachment bytes>
```

The `Content-Type` value must exactly match the value used when requesting the
upload authorization. `If-None-Match: *` makes the generated staging key
write-once and prevents reuse of the URL to overwrite an uploaded object.

Do not manually set `Content-Length`; browsers control it, and Chat verifies the
authoritative object size through R2 after upload. Do not manually set `Host`;
the browser supplies it. Do not send an `Authorization` header to R2 because the
temporary authorization is already encoded in the presigned URL.

## Why the rule is narrow

- `PUT` permits the client-to-R2 upload.
- `GET` permits Flutter Web to fetch an authorized presigned download URL. It
  can be removed if browser clients never fetch downloads through JavaScript.
- `Content-Type` is signed by Chat and required on upload.
- `If-None-Match` is signed by Chat and enforces write-once behavior.
- `HEAD` is unnecessary because Chat performs object verification server-side,
  where browser CORS does not apply.
- `POST`, `PATCH`, `DELETE`, and arbitrary request headers are unnecessary and
  should not be allowed.

Do not configure `AllowedOrigins` as `*`. List the exact trusted Flutter Web
origins separately for staging and production. The R2 staging origins will
normally match Chat's staging `ws-allowed-origins` list when the same frontend
uses both APIs, but the two settings remain independently enforced.

## Deployment checklist

- Configure the rule only on the private chat attachment bucket.
- Use exact HTTPS origins for deployed web clients.
- Add localhost origins only where development access is required.
- Verify that browser preflight permits `PUT`, `Content-Type`, and
  `If-None-Match`.
- Verify that an unlisted browser origin is rejected.
- Verify that uploads fail when a required returned header is omitted or
  changed.
- Never expose the R2 access key ID or secret access key to Flutter.
- Never place a presigned URL in message content, notification payloads, logs,
  or durable attachment records.

See the official [Cloudflare R2 CORS documentation](https://developers.cloudflare.com/r2/buckets/cors/)
for dashboard and API configuration details.
