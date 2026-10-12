### 🐛 Issue #280 — MiMo and every other provider vanished from the topology map, and the Test button faked usage

Two defects made a provider the operator never used look permanently busy on
`Usage → Overview`, and they compounded each other.

- **The "Test" button metered probes as traffic.** `POST /api/models/test` is
  dispatched straight into the chat handler with a probe context
  (`media/model_test_ping.go:84`), and the probe context is honoured at every
  other side effect in the forwarding path — connection unlocks, cooldown
  clears, model-lock releases, fallback skips. The one gap was the usage write:
  `tryForwardWithConnection` called `logUsage` outside the
  `IsProbeContext` guard, so a single click wrote a real `usageHistory` row and
  a `usageDaily.byProvider` bucket. A provider with a passing test appeared to
  have served a request nobody sent, and its tokens landed in the daily totals
  and the est. cost. "Check all models" multiplies this by the catalog size.
  `tryForwardWithConnection` now returns early on a probe, before any usage is
  written — the request still reaches the provider, so the button still works.

- **The topology map drew every configured connection, forever.** The map is
  built client-side in `AnalyticsView` from five sources, and four of them are
  usage-windowed through `stats.byProvider`. The fifth was not: the connections
  loop drew any non-inactive `providerConnections` row. Since that list is the
  whole table, a provider the operator had merely configured sat on the map
  regardless of the selected window, and outlived both the connection and the
  period. It is now gated on the same window as every other source. The lookup
  resolves a connection's stored id against the catalog's canonical id and its
  published alias, because the backend keys usage canonically while a
  connection row may hold either.

Verified in a real browser against a running gateway seeded with an active
`xiaomi-mimo` connection and zero traffic: the map rendered the empty state
("No providers connected") where it previously drew a MiMo node. The probe fix
is covered by `internal/integration/model_test_probe_usage_test.go`, which also
asserts the probe still reaches the fake upstream — a guard that short-circuited
before the request would pass a usage-only test while breaking the button.