### Fixed

- A plan's allowlist is part of a key's policy, so failing to read it now fails
  closed instead of silently dropping the plan's grant. `withPlanPatterns`
  discarded both plan-read errors (`GetPlan`, `GetPlansForKeys`) and fell back to
  the key's own rows; for a key whose restriction lives only in its plan those
  rows are empty, and an empty pattern set reads as "no allowlist configured", so
  a broken plan read turned a restricted key into an unrestricted one —
  `modelAllowed(claude-3-opus) = true`, the published `/v1/models` listing
  returned the whole catalogue, and a 403 became a 400. The chat lanes now return
  `ErrPolicyUnavailable` (503) and the listing and capacity-adapter loaders
  return `SentinelDenyAll` (`!*`), which is what the own-rows half of the same
  function already did for the same class of failure.
- A `planId` that names a plan row which is not there is treated as a failed read,
  not as "no plan". A plan deleted out of band (a restore, a direct DB edit,
  `SetApiKeyPlan`) leaves the key pointing at nothing: `GetPlan` returns
  `sql.ErrNoRows`, and the joined read returns no row at all. Both now surface as
  a failure, so the plan's grant cannot disappear while the key keeps working.
  The key-id-only path reads the key's `planId` to tell "no plan" apart from
  "plan that is not there", because the join alone cannot.
