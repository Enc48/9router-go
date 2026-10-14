### OpenCode: free tier moved to the /inference/v1 gate with the v2 client fingerprint

Upstream decolua/9router#4842 parity (also decolua/9router#4837, #4839, #4780): OpenCode v2
moved free inference off /zen/v1 on 2026-10-14; every free request there now answers 403
FreeTierError, including the real CLI when pointed at /zen/v1.

- The free-tier executor dials /inference/v1/{chat/completions,responses}; a base URL
  carrying the /zen/v1 prefix is rewritten, custom endpoints stay untouched, and relays
  carry the lane in x-relay-path.
- Fingerprint headers follow the v2 CLI: User-Agent opencode/<channel>/<version>/<client>
  (v1-style UAs are replaced), x-opencode-session-id, x-session-affinity and x-session-id
  mirror the session, and the rejected x-opencode-request is no longer sent.
- Translated session ids keep a small counter: the 12-hex prefix now derives only the
  36-bit time field from the digest and pins the counter to 1, as the CLI does, so
  non-OpenCode clients (Claude Code, Codex, curl) stop tripping the gate (#4780).
- The request body declares OpenCode's exact read and shell tools — verbatim
  descriptions and parameter schemas from the v2.0.26 CLI. A caller tool that already
  uses one of those names with a different definition travels under a *_client alias
  (prior calls in history and an explicit tool_choice are retargeted too), and the
  response side restores the caller's spelling. Injected placeholders are marked with a
  short system note so the model keeps using the caller's own tools.
- opencode-zen free models (deepseek-v4-flash-free, muse-spark-*-contributor-free,
  big-pickle, ...) take the same /inference/v1 prefix on their lane; paid zen models keep
  /zen/v1. The classification is catalog-driven (providers.IsOpencodeFreeTierModel), not
  suffix matching.

Verified with `go test ./internal/proxy/... ./internal/translator/... -count=1`: new
routing cases pin the inference path for free chat and responses lanes, the relay lane
prefix, the alias/history/choice rewrite, and the session-counter shape.
