# Staging Origins and R2 CORS

This document contains the browser-origin configuration required by Chat in
staging. Keep the Chat HTTP/WebSocket allowlist and the private R2 bucket CORS
policy aligned for every browser frontend that talks to staging.

Native Android and iOS clients normally omit the `Origin` header and are not
subject to browser CORS. Flutter Web and other browser clients are subject to
these rules.

## Chat origin allowlist

Chat reads the following AWS Systems Manager Parameter Store value in staging:

```text
/yevmiye/staging/ws-allowed-origins
```

The value is a comma-separated list of exact browser origins. An origin
contains only the scheme, hostname, and optional port. Do not include a path,
query string, fragment, or trailing slash.

One staging web frontend:

```text
https://staging.yevmiye.com
```

Multiple staging frontends:

```text
https://staging.yevmiye.com,https://admin-staging.yevmiye.com
```

Staging plus a local Flutter Web development server:

```text
https://staging.yevmiye.com,http://localhost:3000
```

Example AWS CLI command:

```bash
aws ssm put-parameter \
  --name /yevmiye/staging/ws-allowed-origins \
  --type String \
  --value 'https://staging.yevmiye.com,http://localhost:3000' \
  --overwrite
```

Chat applies this allowlist to HTTP CORS, including `POST /ws/ticket`, and to
the WebSocket origin check. The browser origin must match exactly. Do not use a
wildcard for staging private-chat traffic.

## Private R2 bucket upload CORS

Configure the private Chat R2 bucket with this policy for one staging web
frontend:

```json
[
  {
    "AllowedOrigins": [
      "https://staging.yevmiye.com"
    ],
    "AllowedMethods": [
      "PUT"
    ],
    "AllowedHeaders": [
      "Content-Type",
      "If-None-Match"
    ],
    "ExposeHeaders": [
      "ETag"
    ],
    "MaxAgeSeconds": 3600
  }
]
```

For staging plus local Flutter Web development:

```json
[
  {
    "AllowedOrigins": [
      "https://staging.yevmiye.com",
      "http://localhost:3000"
    ],
    "AllowedMethods": [
      "PUT"
    ],
    "AllowedHeaders": [
      "Content-Type",
      "If-None-Match"
    ],
    "ExposeHeaders": [
      "ETag"
    ],
    "MaxAgeSeconds": 3600
  }
]
```

The client must send every header returned by Chat's upload-authorization
response. In the current contract, the presigned PUT requires `Content-Type`
and `If-None-Match: *`. Do not add `Content-Length` as a manually supplied or
required browser header.

Do not configure `AllowedOrigins` as `*`. A presigned URL is a temporary bearer
credential; CORS limits which browser origins may use it, but it does not
replace Chat's participant authorization, short expiry, write-once condition,
or confirmation-time R2 metadata verification.

This policy covers direct browser uploads. If a future web client fetches
presigned downloads through JavaScript instead of using the current
download-style navigation, review and explicitly add the required `GET` CORS
policy at that time.
