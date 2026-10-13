### service: five follow-ups from PR #303 (issue #304)

Tech debt recorded during the review of `feat(cli): service autostart management`.
None of these changed runtime behaviour on Linux or macOS, but all five were
wrong in a way a user could hit.

- **`WorkingDirectory=` is now escaped.** `ExecStart` already went through
  `commandLine`, but the neighbouring line wrote the directory raw. systemd
  unquotes the assignment rather than splitting on whitespace, so a binary
  installed under `/opt/my apps/` produced a unit that failed to parse. The
  value now goes through `quoteSystemdValue`, which escapes what systemd's
  unquoter actually interprets — a backslash or quote (C escapes inside double
  quotes) and `%` (every assignment is specifier-expanded, so a literal percent
  is `%%`). The path is converted with `filepath.ToSlash`, which also makes the
  rendered unit identical on a Windows test runner, where `filepath.Dir` was
  returning `\usr\local\bin`.
- **`enable-linger` now runs even when `systemctl start` fails.** The early
  return on the failure path skipped it, so the one scenario install exists for
  — the gateway already holding the port from `9router-go start` — lost linger
  and the unit died at logout while the CLI reported the entry as enabled.
- **`uninstallCron` no longer claims a removal that failed.** `os.Remove`'s
  error was discarded and `desktopRemoved` was set unconditionally, so a
  permission-denied delete printed "Autostart uninstalled" with
  `~/.config/autostart/9router-go.desktop` still in place and set to fire at the
  next login. Removal is now the source of truth, the rule the launchd and
  OpenRC backends in the same package already followed.
- **Windows task status is locale-independent.** `statusWindowsTask` matched the
  literal `Running` in `schtasks /query` output, which follows the system
  locale: on a non-English Windows the literal never appears and `service
  status` reported `Active: false` for a task that was running. The state now
  comes from `Get-ScheduledTask` as a number, so it does not depend on the UI
  language. When that probe cannot answer — PowerShell absent, or blocked by
  execution policy — the new `Info.ActiveUnknown` makes `service status` print
  `Active: unknown` instead of a `false` that is a guess presented as fact.
- **Three `internal/service` tests now pass on Windows.** CI only ran
  ubuntu-latest, so the failure was invisible: one assertion compared against
  a POSIX path that `filepath.Dir` could not produce on Windows, and two needed
  `SeCreateSymbolicLinkPrivilege` to build their runit activation symlink.
  `os.Symlink` and `os.Remove` are now seams, the way `readCrontab`/`writeCrontab`
  already were, so the runit activation path is asserted as a request on runners
  without symlink privilege instead of failing. No assertion coverage was
  dropped.

Verified with `go test -count=1 ./internal/service/` green on Windows, the
built binary's `service status` run end to end, and the new
`windows-service` CI job added to `.github/workflows/ci.yml` so this class of
OS-conditional regression is caught on a PR rather than on someone's laptop.
