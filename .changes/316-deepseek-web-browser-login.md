### feat(deepseek-web): route chat.deepseek.com with a browser-login userToken

Adds the first provider in this build authenticated by a credential a browser
session produces rather than an API key or an OAuth grant, ported from OmniRoute
`open-sse/executors/deepseek-web.ts`. The credential is the `userToken` from
`chat.deepseek.com` → DevTools → Application → Local Storage.

**Why:** the DeepSeek account a user already has is a free consumer web account,
not an API plan, and it has been unusable here. Every other browser-login
provider in this repo was worse than absent — see the removal note below.

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
  and rotates the account instead of retrying a dead session. Confirmed against
  the live endpoint: a rejected credential answers
  `{"code":40003,"msg":"Authorization Failed (invalid token)"}` with HTTP 200.
- A stream that closes without the upstream's `FINISHED` signal is reported as
  an error, not as a completed turn. Serving the partial text with
  `finish_reason: stop` is how a client comes to treat a truncated reply as the
  model's conclusion.
- `Origin` and `Referer` follow the connection's configured base URL, so a
  `baseUrl` override is honoured end to end instead of only for the completion
  call.
- `deepseek-web` is a separate entry from `deepseek`, with its own alias
  (`ds-web`) and its own model catalogue — pinned by a test, since a cross-alias
  would send a browser credential to an API endpoint.
- `AddConnectionModal` now persists `authType: 'cookie'` for a browser-login
  provider instead of `'apikey'`, and shows a placeholder that names the actual
  localStorage entry. The probe dispatcher already branched on `cookie`, so
  every cookie provider was taking the wrong probe path.

### fix(providers): remove the browser-login providers that cannot work here

`grok-web` and `perplexity-web` were catalogued with dashboard probes but had
**no executor at all**, so a chat request fell through to the shared OpenAI
passthrough and sent `Authorization: Bearer <cookie>` to a site that answers
401. Neither had ever worked. Both need TLS fingerprint impersonation
(`wreq-js`, `chrome_146`) in OmniRoute, which this build cannot do and will not
add a CGO dependency for.

A provider that cannot route but accepts a credential is worse than a missing
one: the user pastes a cookie, gets a 401 that reads as "expired", and re-pastes
correctly-entered credentials in a loop. Their registry entries, aliases, model
lists, probes, probe tests and dashboard artwork are removed. Anyone who wants
them should run a build that embeds a browser.

`gemini-web` never shipped as working and is removed for the same reason:
`gemini.google.com` has no API the web client calls directly, and upstream drives
it by typing into a Chromium page.

`scripts/deepseek-web-live.sh` runs one real turn with an operator-supplied
credential, reading it from a gitignored file so it never enters a repository or
a chat transcript. `DS_BASE_URL` points it at a local fake upstream, which is
how every step of the harness itself was exercised.

**Verification:** `go build ./...` and `go vet ./...` clean.
`deepseek_web_pow_test.go` pins the 23-round permutation against OmniRoute's own
vectors (`deepseek-pow-js-only.test.ts`) plus the block-boundary cases those
vectors never reach. `deepseek_web_test.go` drives a whole turn against one
`httptest` upstream and verifies the call order, that the proof-of-work nonce
really satisfies the challenge, the browser fingerprint headers, both response
shapes, and that a truncated stream is not served as complete.
`validate_web_providers_test.go` covers the probe's two misleading answers: a
rejected credential arriving as HTTP 200, and a 200 carrying no token.
`deepseek_web_catalog_test.go` pins the catalog and the provider isolation.
Live: the credential exchange, session creation and proof-of-work were confirmed
against the real endpoint — the nonce for a difficulty-144000 challenge is found
in ~400ms. The completion itself could not be exercised because DeepSeek's status
page showed an active outage (incident 7116348214287, "Identified") throughout,
so the stream translator's handling of a healthy response remains unproven
against the live site. `bun run build`, 344 web tests and `make vet-svelte`
(0 unresolved identifiers) pass.