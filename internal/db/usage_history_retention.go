package db

import (
	"context"
	"fmt"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Opt-in retention for the billing ledger (#392).
//
// usageHistory holds one row per request and, unlike requestDetails, is not
// pruned by default: it is the source of a per-key token budget, and the `all`
// usage period reads it directly. A gateway that has been running for a year
// therefore carries every row it ever wrote, with no knob to drop the part an
// operator does not need.
//
// The hard part is not the DELETE, it is what a DELETE does to a budget.
// tokenQuota is measured by summing promptTokens+completionTokens over
// usageHistory after the key's reset mark, so deleting old rows silently
// refunds spend — an exhausted key becomes unexhausted. The fix keeps the sum
// exact without introducing a counter that could drift: before a range is
// deleted, the tokens it contributed are banked per key in
// usageLedgerCarryover, and tokenUsageSince adds that term back.
//
// usageDaily, requestDetails and compressionAnalytics are separate tables and
// are deliberately untouched here.

// MinUsageHistoryRetention is the shortest ledger window this build will prune.
//
// The bound is not arbitrary. The dashboard's raw usage cache ages rows out of a
// sliding window by reading them back from the ledger and subtracting them
// (usage_window_cache.go, subtractOutOfWindow), which is exact only while those
// rows still exist. A retention shorter than the window a client is watching
// would delete rows the cache still owes a subtraction for, and the totals would
// read high until the cache rebuilt. 24h is the widest sliding preset the
// dashboard ships, so a window at or above it can never race the prune.
const MinUsageHistoryRetention = 24 * time.Hour

// UsageHistoryPruneResult reports one ledger prune.
type UsageHistoryPruneResult struct {
	// Rows is how many usageHistory rows were (or would be) deleted.
	Rows int `json:"rows"`
	// Tokens is the prompt+completion total those rows contributed to a key's
	// budget, i.e. how much spend was banked as carryover rather than lost.
	Tokens int64 `json:"tokens"`
	// DryRun reports whether anything was actually deleted.
	DryRun bool `json:"dryRun"`
	// Cutoff is the RFC3339 boundary: rows strictly older than this go.
	Cutoff string `json:"cutoff"`
}

// PruneUsageHistory deletes usageHistory rows older than retention in bounded
// chunks, banking their per-key token spend in usageLedgerCarryover first so no
// budget is refunded.
//
// A non-positive retention is refused rather than treated as "keep nothing":
// the only value that means "keep forever" is the setting being off, and an
// accidental 0 here must not wipe the ledger.
func (r *Repo) PruneUsageHistory(ctx context.Context, retention time.Duration, dryRun bool) (UsageHistoryPruneResult, error) {
	result := UsageHistoryPruneResult{DryRun: dryRun}
	if r == nil || r.db == nil {
		return result, fmt.Errorf("prune usageHistory: nil repo")
	}
	if ctx == nil {
		return result, fmt.Errorf("prune usageHistory: nil context")
	}
	if retention <= 0 {
		return result, fmt.Errorf("prune usageHistory: retention must be positive, got %v", retention)
	}
	if retention < MinUsageHistoryRetention {
		return result, fmt.Errorf("prune usageHistory: retention %v is below the %v minimum",
			retention, MinUsageHistoryRetention)
	}

	// usageHistory.timestamp is written as RFC3339 (second precision, see
	// insertUsageHistory), so the cutoff must use the same layout or the string
	// comparison silently matches nothing.
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339)
	result.Cutoff = cutoff

	if dryRun {
		if err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM usageHistory WHERE timestamp < ?`, cutoff,
		).Scan(&result.Rows); err != nil {
			return result, fmt.Errorf("count stale usageHistory: %w", err)
		}
		return result, nil
	}

	idx, err := r.loadLedgerKeyIndex(ctx)
	if err != nil {
		return result, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("prune usageHistory: %w", err)
		}
		rows, tokens, err := r.pruneUsageHistoryChunk(ctx, cutoff, idx)
		if err != nil {
			return result, err
		}
		result.Rows += rows
		result.Tokens += tokens
		if rows < retentionChunkSize {
			break
		}
	}

	if result.Rows > 0 {
		// The planner's statistics describe a table that just lost a large
		// stretch of its oldest rows; refreshing them is what keeps the
		// analytics queries off a stale plan afterwards.
		if _, err := r.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
			return result, fmt.Errorf("prune usageHistory: pragma optimize: %w", err)
		}
	}
	return result, nil
}

// ledgerKeyIndex maps the two spellings a usage row can carry back to the
// apiKeys row they belong to, so a prune can attribute deleted spend to the
// key whose budget it counted against.
//
// It is a snapshot loaded once per pass rather than a join per chunk: apiKeys
// holds a handful of rows while the ledger holds millions, and the masked form
// is computed in Go (handlerutil.MaskAPIKey), so SQL cannot join on it.
type ledgerKeyIndex struct {
	byRaw    map[string]string
	byMasked map[string]string
	marks    map[string]int64
}

func (r *Repo) loadLedgerKeyIndex(ctx context.Context) (*ledgerKeyIndex, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, key, COALESCE(quotaResetId, 0) FROM apiKeys`)
	if err != nil {
		return nil, fmt.Errorf("prune usageHistory: read api keys: %w", err)
	}
	defer rows.Close()

	idx := &ledgerKeyIndex{
		byRaw:    make(map[string]string),
		byMasked: make(map[string]string),
		marks:    make(map[string]int64),
	}
	for rows.Next() {
		var id, key string
		var mark int64
		if err := rows.Scan(&id, &key, &mark); err != nil {
			return nil, fmt.Errorf("prune usageHistory: scan api key: %w", err)
		}
		if id == "" {
			continue
		}
		idx.byRaw[key] = id
		idx.byMasked[handlerutil.MaskAPIKey(key)] = id
		idx.marks[id] = mark
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("prune usageHistory: read api keys: %w", err)
	}
	return idx, nil
}

// matching returns every key whose budget this row counted against.
//
// It mirrors tokenUsageSince's predicate exactly — `clientKey = masked OR
// apiKey = raw OR apiKey = masked` — because a row that was summed into two
// budgets must have its spend banked for both, or pruning would refund one of
// them. In practice a row carries one client spelling and one upstream
// credential, so the slice is length one; the loop is what makes the carryover
// exact rather than merely usually right.
func (idx *ledgerKeyIndex) matching(clientKey, apiKey string) []string {
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		for _, seen := range ids {
			if seen == id {
				return
			}
		}
		ids = append(ids, id)
	}
	add(idx.byMasked[clientKey])
	add(idx.byRaw[apiKey])
	add(idx.byMasked[apiKey])
	return ids
}

// pruneUsageHistoryChunk deletes the next retentionChunkSize stale rows and
// banks their spend, in one transaction so the rows counted are the rows
// deleted.
//
// The DELETE re-uses the selection's predicate plus `id <= maxID`: the chunk is
// the lowest ids under the cutoff, so that bound names exactly the selected
// rows and needs no 5000-placeholder IN list.
func (r *Repo) pruneUsageHistoryChunk(ctx context.Context, cutoff string, idx *ledgerKeyIndex) (int, int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("prune usageHistory: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(clientKey, ''), COALESCE(apiKey, ''),
		        COALESCE(promptTokens, 0) + COALESCE(completionTokens, 0)
		   FROM usageHistory WHERE timestamp < ? ORDER BY id ASC LIMIT ?`,
		cutoff, retentionChunkSize)
	if err != nil {
		return 0, 0, fmt.Errorf("prune usageHistory: select chunk: %w", err)
	}

	var (
		maxID    int64
		selected int
		banked   = make(map[string]int64)
		tokens   int64
	)
	for rows.Next() {
		var (
			id        int64
			clientKey string
			apiKey    string
			rowTokens int64
		)
		if err := rows.Scan(&id, &clientKey, &apiKey, &rowTokens); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("prune usageHistory: scan chunk: %w", err)
		}
		selected++
		if id > maxID {
			maxID = id
		}
		for _, keyID := range idx.matching(clientKey, apiKey) {
			// Rows at or before the key's reset mark were already excluded
			// from its budget, so they contribute nothing to carry over.
			if id <= idx.marks[keyID] {
				continue
			}
			banked[keyID] += rowTokens
			tokens += rowTokens
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, fmt.Errorf("prune usageHistory: read chunk: %w", err)
	}
	rows.Close()

	if selected == 0 {
		return 0, 0, nil
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM usageHistory WHERE timestamp < ? AND id <= ?`, cutoff, maxID,
	); err != nil {
		return 0, 0, fmt.Errorf("prune usageHistory: delete chunk: %w", err)
	}

	if len(banked) > 0 {
		stamp := time.Now().UTC().Format(time.RFC3339)
		for keyID, add := range banked {
			// Accumulate rather than overwrite: two prunes in a row must bank
			// the second range on top of the first, otherwise the older prune's
			// spend is refunded by the newer one.
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO usageLedgerCarryover(keyId, tokens, updatedAt) VALUES (?, ?, ?)
				 ON CONFLICT(keyId) DO UPDATE SET
				   tokens = usageLedgerCarryover.tokens + excluded.tokens,
				   updatedAt = excluded.updatedAt`,
				keyID, add, stamp,
			); err != nil {
				return 0, 0, fmt.Errorf("prune usageHistory: bank carryover for %s: %w", keyID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("prune usageHistory: commit: %w", err)
	}
	return selected, tokens, nil
}

// UsageHistoryRetention returns the configured ledger window, or 0 when the
// operator has not opted in. 0 means "keep every row forever".
func (r *Repo) UsageHistoryRetention() time.Duration {
	s, err := r.GetSettings()
	if err != nil || s == nil || s.UsageHistoryRetentionDays <= 0 {
		return 0
	}
	return time.Duration(s.UsageHistoryRetentionDays) * 24 * time.Hour
}
