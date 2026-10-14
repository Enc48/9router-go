### OpenCode: free tier moved to the /inference/v1 gate with the v2 client fingerprint

Upstream decolua/9router#4842 parity (Closes #317, supersedes #318 and #319):
OpenCode v2 moved free inference off /zen/v1; every free request there now
answers 403 FreeTierError.

- The keyless free executor dials /inference/v1/{chat/completions,responses}.
  A base URL carrying the /zen/v1 prefix is rewritten host-agnostically,
  custom endpoints stay untouched, and relays carry the lane in x-relay-path.
- Fingerprint headers follow the v2 CLI: User-Agent opencode/<channel>/
  <version>/<client> with v1 shapes rejected, x-opencode-session-id,
  x-session-affinity and x-session-id mirror one session (a downstream hint
  moves all four together), the rejected x-opencode-request is never sent,
  and the project defaults to "global" like upstream.
- Session ids keep a small counter: generated and translated prefixes pin the
  counter to 1 exactly like the CLI, so non-OpenCode clients stop tripping
  the gate under concurrent load.
- The request body declares OpenCode's exact read and shell tools — verbatim
  descriptions and parameter schemas from the v2 CLI, strict:false on the
  Responses lane. A caller tool reusing one of those names with a different
  definition travels under a *_client alias (history and tool_choice
  retargeted); appended placeholders get the upstream singular/plural note,
  never on Claude-shaped bodies. tool_choice passes through untouched except
  on the Responses lane, where auto-only models collapse to "auto" and a
  forced choice naming no declared tool is dropped.
- Paid lanes are deliberately untouched: opencode-zen stays on /zen/v1 with
  the v1 identity and quartet cloaking, and opencode-go keeps its predicate,
  v1 UA and request ids. Upstream #4842 leaves every keyed executor as-is,
  so keyed requests (including free ids on zen connections) are never
  rerouted to the keyless gate.

Verified with `go test -count=1 ./internal/proxy/ ./internal/proxy/executor/
./internal/translator/ ./internal/providers/ ./internal/handlers/chat/`:
verb gate-contract pins (verbatim schemas, strict:false, note wording),
lane predicates for free vs go, v1 identity on paid lanes, the zen keyed-lane
guard, session mirrors, and the mock E2E shapes.
