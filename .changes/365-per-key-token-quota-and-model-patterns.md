### feat(access): allowlist globs and deny rules for per-key model access

Per-key model allowlists gain the pattern vocabulary issue #365 asks for, ported
from decolua/9router `keyAccess.js` (v0.5.104): a wildcard that spans the `/`
(`*claude*`), deny entries (`!opus`, `-haiku`), and a deny-only list that opens
everything it does not name.

**Why:** the allowlist matched through the resolver's own glob
(`providers.MatchModelPattern`, backed by `path.Match`), where `*` is
segment-local. An operator writing `*claude*` — the spelling the issue and
upstream both use — got a key that denied every model, and there was no way to
express "everything except X" short of enumerating the catalogue. Measured
before the change: 7 of 17 cases in `TestModelAllowedPatterns` failed, among
them `*claude*` against `anthropic/claude-sonnet-4` and every deny-only list.

**What changed:**

- `providers.MatchAllowPattern` — the allowlist glob. `*` spans `/`, matching is
  case-insensitive and anchored at both ends, the pattern is literal except for
  `*`, and a pattern without `*` still answers exactly as before.
  `MatchModelPattern` is untouched so the resolver's capability patterns keep
  their segment-local semantics; a test pins that the two cannot be collapsed
  into one by accident.
- `providers.IsDenyEntry` / `providers.MatchDenyEntry` — `!term` / `-term`
  parsing (a lone sigil is a model id, not a rule) and deny matching, which
  accepts a glob or a substring so `-haiku` needs no asterisk.
- `chat.modelAllowed` — parses the list into positives and denies, checks denies
  first so a deny always wins over a positive rule, and reads a list with no
  positive entry as deny-only: open except denied. An empty or blank-only list
  still means "no allowlist configured" and allows everything, which is what
  keeps every key minted before this feature behaving as it did.
- Dispatch and listing still share the one decision function, so a denied model
  is hidden from `/v1/models` as well as refused at `/v1/chat/completions`.

**Verified:** `go test ./internal/providers/ ./internal/handlers/chat/` — both
packages ok; the 7 previously failing cases are green alongside 40 new matcher
cases.
