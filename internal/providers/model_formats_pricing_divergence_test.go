package providers

import (
	"sort"
	"strings"
	"testing"

	"9router/proxy/internal/pricing"
)

// Price and lane are two different questions that share a vocabulary, and
// nothing used to force them to agree (#410):
//
//   - pricing.KnownFreeModels answers "what does this id cost" — an id there is
//     billed at zero although it carries no "-free" suffix.
//   - openCodeFreeTierModels answers "which gate serves this id" — an id there is
//     forwarded to the keyless /inference/v1 gate instead of the keyed /zen/v1
//     lane.
//
// An id may legitimately land in one and not the other, but only as a recorded
// decision. openCodePricingDivergences is that record: every id whose two answers
// disagree names which side is deliberate and why. The tests below fail when a new
// id starts disagreeing silently, and when a recorded entry stops being true.
var openCodePricingDivergences = map[string]string{
	"big-pickle": "price: zero, mirroring upstream's suggested-models filter. lane: keyed /zen/v1, paid — #328 pinned it after a 200 from the keyless gate failed to separate \"this id is free\" from \"the gate served a zero-priced id\", and #366 declined to move it. Billing zero for a paid-lane call is the open question in #410; these two lists cannot settle it.",
}

// declaresFree reports whether an id states its own free status in its name. Such
// an id is free on both sides by convention — the price table carries no row for
// it and the lane rule matches the suffix — so it cannot drift.
func declaresFree(id string) bool {
	return strings.HasSuffix(strings.ToLower(id), "-free")
}

// TestZeroPricedIdWithoutFreeLaneIsDocumented covers the price→lane direction: an
// id billed at zero that is not routed to the free gate must say so in the record,
// and one that is routed there must not still carry an entry.
func TestZeroPricedIdWithoutFreeLaneIsDocumented(t *testing.T) {
	ids := append([]string(nil), pricing.KnownFreeModels...)
	sort.Strings(ids)
	if len(ids) == 0 {
		t.Skip("no zero-priced ids outside the -free convention; nothing to reconcile")
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if !pricing.IsFreeModel(id) {
				t.Fatalf("%q is listed in KnownFreeModels but IsFreeModel is false", id)
			}
			laned := IsOpencodeFreeTierModel("opencode-zen", id)
			_, documented := openCodePricingDivergences[id]
			if laned == documented {
				t.Errorf("IsOpencodeFreeTierModel(opencode-zen, %q) = %v and documented = %v: "+
					"either the lists now agree (drop the entry) or they disagree without a recorded reason (add one)",
					id, laned, documented)
			}
		})
	}
}

// TestFreeLaneIdWithoutZeroPriceIsDocumented covers the lane→price direction: an
// id routed to the free gate whose name does not declare it free must be priced at
// zero or recorded as a divergence.
func TestFreeLaneIdWithoutZeroPriceIsDocumented(t *testing.T) {
	for id := range openCodeFreeTierModels {
		if declaresFree(id) {
			continue
		}
		t.Run(id, func(t *testing.T) {
			if pricing.IsFreeModel(id) {
				return
			}
			if _, ok := openCodePricingDivergences[id]; !ok {
				t.Errorf("%q is routed to the free gate but not billed at zero; record the reason in openCodePricingDivergences", id)
			}
		})
	}
}

// TestRecordedDivergencesStillHold keeps the record honest: an entry that no
// longer describes a disagreement is stale and must be dropped, so the list stays
// a statement about the code rather than a place entries go to be forgotten.
func TestRecordedDivergencesStillHold(t *testing.T) {
	ids := make([]string, 0, len(openCodePricingDivergences))
	for id := range openCodePricingDivergences {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if len(ids) == 0 {
		t.Fatal("the record is empty, so no divergence is covered — remove this test only together with the map")
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if reason := openCodePricingDivergences[id]; strings.TrimSpace(reason) == "" {
				t.Errorf("%q is recorded without a reason", id)
			}
			if !pricing.IsFreeModel(id) {
				t.Errorf("%q is recorded as billed at zero but pricing.IsFreeModel is false; drop the entry", id)
			}
			if IsOpencodeFreeTierModel("opencode-zen", id) {
				t.Errorf("%q is recorded as paid-lane but now routes to the free gate; drop the entry", id)
			}
		})
	}
}
