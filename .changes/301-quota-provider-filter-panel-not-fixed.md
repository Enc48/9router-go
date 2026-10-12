### 🐛 The Quota Tracker provider filter pushed the page down instead of opening over it

Opening "All providers" on the Quota Tracker shoved the quota cards out of the
way instead of overlaying them, which is what made the page look broken.

The provider panel was the only dropdown in the dashboard that measured itself
against its trigger and wrote `left` / `top` inline while **not** being
`position: fixed` and not being portalled. The computed position came back
`static`, so the placement maths was dead output: `left: 896px; top: 140px` was
written onto a plain in-flow `<div>`, and `placePanel` / `placementStyle` were
doing work no browser honoured.

Everything around it was already correct and gave the bug away on re-reading —
the click-outside handler checks `providerDropdownPanel.contains(target)`
precisely because the panel is expected to be detached, the resize and scroll
listeners already reposition it, and a comment states the panel *is* fixed. Only
the element itself was missing the two attributes every sibling panel has:

| Panel | `fixed` | `use:portal` |
| :--- | :---: | :---: |
| `SectionMenu`, `CountedSelect`, `Menu`, `ViewSelect` | ✅ | ✅ |
| `QuotaTrackerView` provider filter | ❌ | ❌ |

The panel now carries the same `use:portal` + `fixed z-50` pair the other five
use, so it is anchored to the window rather than to the toolbar row, and the
long provider list overlays the cards instead of pushing them.

Verified in a browser against a running gateway: computed `position` is `fixed`
and the parent is `BODY`; selecting an option still filters (label and row
update); a click on the panel's own background leaves it open while a click
outside closes it, so the click-outside handler still resolves through the
detached node; and at a 390px phone width the panel measures 256px wide with
both edges on screen.