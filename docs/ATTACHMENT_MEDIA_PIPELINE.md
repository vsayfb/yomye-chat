# Private attachment media pipeline

Chat stores attachment objects in the private Cloudflare R2 bucket and never proxies attachment bytes. Clients request a short-lived, write-once signed upload, upload directly to R2, and then confirm the object. Confirmation re-verifies the stored object's content type and exact size before the attachment becomes usable. Downloads remain short-lived signed URLs authorized against current conversation participation.

Image bandwidth optimization is intentionally performed by the client before it requests the signed upload. The client corrects EXIF orientation, bounds oversized still images to a 2048 px maximum edge, removes unnecessary metadata by re-encoding the derivative, and preserves alpha-capable PNG images. GIF files are not decoded/re-encoded so animation is not accidentally flattened. Chat still applies the authoritative attachment size/type policy to the derivative metadata and again to the object in R2; client preprocessing is not a security boundary.

Chat video remains direct-to-R2 with the existing size and 60-second duration policy. Cross-platform video compression is not performed in this change because the current Flutter dependency set has no reliable web/mobile compressor. A future asynchronous processor or Cloudflare Stream integration should create optimized playback variants without making the source bucket public.
