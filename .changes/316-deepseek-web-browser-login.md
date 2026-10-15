### feat(deepseek-web): route chat.deepseek.com with a browser-login userToken

Adds the first provider in this build authenticated by a credential a browser
session produces rather than an API key or an OAuth grant, ported from OmniRoute
`open-sse/executors/deepseek-web.ts`. The credential is the `userToken` from
`chat.deepseek.com` → DevTools → Application → Local Storage.

**Why:** the deepseek account a user already has is a free consumer web account,
not an API plan, and it has been unusable here. `grok-web` and `perplexity-web`
were already catalogued but had no executor at all, so a chat request fell
through to the shared passthrough and sent `Authorization: Bearer <cookie>` to a
site that answers 401.

**What it does, per turn:**

- `GET /api/v0/users/current` exchanges the `userToken` for a short-lived Bearer
  access token, memoized an hour and dropped on any auth failure. A pasted
  `{"value":"…"}` localStorage entry is unwrapped, which is the shape a copy out
  of DevTools actually has.
- `POST /api/v0/chat_session/create` opens a session, closed again once the turn
  is done.
- `POST /api/v0/chat/create_pow_challenge` → a **DeepSeekHashV1** proof of work
  is solved and returned in `X-Ds-Pow-Response`. That hash is SHA3-256 with the
  last 23 KECCAK-p[1600] rounds; Go's `crypto/sha3` only exposes 24, so the
  sponge is implemented in `deepseek_web_pow.go` with the prefix absorbed once and
  reused per candidate, bounded by a 30s deadline and cancellable from the
  request context.
- The upstream event stream is translated to OpenAI chunks, keeping the
  reasoning and answer channels separate and folding search citations into the
  answer.

**Details worth knowing:**

- This API reports auth and rate limiting as **HTTP 200 with a code in the
  body** (40003 / 40002). Both are mapped to 401 / 429, so the router cools down
  and rotates the account instead of retrying a dead session.
- A stream that closes without the upstream's `FINISHED` signal is reported as
  an error, not as a completed turn. Serving the partial text with
  `finish_reason: stop` is how a client comes to treat a truncated reply as the
  model's conclusion.
- `Origin` and `Referer` follow the connection's configured base URL, so a
  `baseUrl` override is honoured end to end instead of only for the completion
  call.
- `deepseek-web` and `gemini-web` are separate entries from `deepseek` and
  `gemini`, with their own aliases (`ds-web`, `gweb`) and their own model
  catalogues — pinned by a test, since a cross-alias would send a browser
  credential to an API endpoint.
- `AddConnectionModal` now persists `authType: 'cookie'` for a browser-login
  provider instead of `'apikey'`. The probe dispatcher already branched on
  `cookie`, so every cookie provider was taking the wrong probe path.

**gemini-web is catalog-only.** `gemini.google.com` has no API the web client
calls directly — upstream drives it with an embedded Chromium — and this build
ships no browser. It is listable and manageable; a chat request is refused with
501 and the reason, and the probe says it cannot check rather than reporting a
valid cookie as invalid.

**Verification:** `go build ./...` and `go vet ./...` clean.
`internal/proxy/executor/deepseek_web_pow_test.go` pins the 23-round permutation
against OmniRoute's own vectors (`deepseek-pow-js-only.test.ts`) plus the block
boundary cases those vectors never reach.
`deepseek_web_test.go` drives a whole turn against one `httptest` upstream and
verifies the call order, that the proof-of-work nonce really satisfies the
challenge, the browser fingerprint headers, both response shapes, and that a
truncated stream is not served as complete.
`validate_web_providers_test.go` covers the probe's two misleading answers: a
rejected credential arriving as HTTP 200, and a 200 carrying no token.
`deepseek_web_catalog_test.go` pins the catalog and the provider isolation.
`bun run build`, 178 web tests and `make vet-svelte` (0 unresolved identifiers)
pass.