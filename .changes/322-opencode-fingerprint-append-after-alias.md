### OpenCode: append the exact fingerprint even when caller tools collide

Follow-up to #320, root-caused live: a TUI-shaped body whose `read`/`shell`
tools carry non-CLI definitions got its tools aliased (`read_client`, ...)
but no exact `read`/`shell` declaration traveled, and the gate answered 403
"free tier can only be used from within OpenCode" — reproduced against the
real gate (403 before, 200 after, same body).

- The append check now runs on post-alias names like upstream's
  `appendMissingFingerprintTools`, instead of the pre-alias seen set.
- Pinned with `TestConcealOpenCodeV2CollidingNamesStillAppendExact`
  (order `read_client2,read_client,Edit,read,shell`, both lanes).

Verified with `go test -count=1 ./internal/translator/ ./internal/proxy/
/internal/proxy/executor/ ./internal/handlers/chat/` and two live gate
probes (minimal + colliding-tools bodies, cli and desktop identities).
