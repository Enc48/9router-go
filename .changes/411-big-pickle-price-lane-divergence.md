### Pricing and lane disagreement for `big-pickle` is recorded, not silent

- `pricing.KnownFreeModels` answers "what does it cost" while
  `providers.openCodeFreeTierModels` answers "which gate serves it", and `big-pickle`
  sits in the first without the second: billed at zero, routed on the keyed paid
  `/zen/v1` lane (#410). Both sides are deliberate — #328 pinned the lane and #366
  declined to move it — so this change alters neither behaviour; it makes the
  disagreement explicit and guards it.
- `internal/providers/model_formats_pricing_divergence_test.go` adds the missing
  guard. `TestZeroPricedIdWithoutFreeLaneIsDocumented` fails when an id is billed at
  zero but not routed to the free gate, or when the record still lists an id whose
  lists now agree; `TestFreeLaneIdWithoutZeroPriceIsDocumented` covers the opposite
  direction; `TestRecordedDivergencesStillHold` fails when a recorded entry stops
  describing a real disagreement. A new id can no longer land in one list without a
  decision about the other.
- Both lists now state which question they answer in their doc comments, so the next
  reader does not edit one to match the other.
- Verified with `go build ./...`, `go vet` on both packages, and the full
  `go test ./...`. The guard was proven to bite: adding an unlisted id to
  `KnownFreeModels` makes `TestZeroPricedIdWithoutFreeLaneIsDocumented` fail with
  `documented = false`, and reverting restores green.
