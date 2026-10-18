package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Pruning the billing ledger must not refund a budget. These tests pin the
// property the issue (#392) is about: a key's GetApiKeyTokenUsage is identical
// before and after a prune that deletes rows inside its budget range, and a
// reset still starts it from zero afterwards.

// seedLedgerRow inserts one usageHistory row at an exact timestamp.
//
// InsertUsageHistory stamps time.Now(), which is useless for a retention test,
// so the timestamp is written directly. clientKey is the spelling this gateway
// attributes a client with; apiKey is the upstream credential.
func seedLedgerRow(t *testing.T, repo *Repo, at time.Time, apiKey, clientKey string, prompt, completion int) int64 {
	t.Helper()
	res, err := repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey,
		                           endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		 VALUES (?, 'openai', 'gpt-4o', 'c1', ?, ?, '/v1/chat/completions', ?, ?, 0, 'success', ?, '{}')`,
		at.UTC().Format(time.RFC3339), apiKey, clientKey, prompt, completion,
		prompt+completion)
	if err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed usageHistory id: %v", err)
	}
	return id
}

func carryoverFor(t *testing.T, repo *Repo, keyID string) int64 {
	t.Helper()
	var tokens int64
	err := repo.RawDB().QueryRow(
		`SELECT COALESCE(tokens, 0) FROM usageLedgerCarryover WHERE keyId = ?`, keyID).Scan(&tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatalf("read carryover for %s: %v", keyID, err)
	}
	return tokens
}

// TestPruneUsageHistoryKeepsBudgetExact is the headline acceptance criterion: a
// prune that removes rows inside a key's budget range must not change what that
// key is charged.
func TestPruneUsageHistoryKeepsBudgetExact(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := t.Context()

	const rawKey = "sk-prune-budget-key-0001"
	if err := repo.CreateApiKey("kr", rawKey, "budget key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := repo.SetApiKeyBudget("kr", 1000, 0); err != nil {
		t.Fatalf("SetApiKeyBudget: %v", err)
	}

	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-24 * time.Hour)
	seedLedgerRow(t, repo, old, handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 100, 50)
	seedLedgerRow(t, repo, recent, handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 200, 25)

	before, err := repo.GetApiKeyTokenUsageByID(ctx, "kr")
	if err != nil {
		t.Fatalf("usage before prune: %v", err)
	}
	if before != 375 {
		t.Fatalf("usage before prune = %d, want 375", before)
	}

	result, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	if result.Rows != 1 {
		t.Errorf("pruned rows = %d, want 1", result.Rows)
	}
	if result.Tokens != 150 {
		t.Errorf("banked tokens = %d, want 150", result.Tokens)
	}

	after, err := repo.GetApiKeyTokenUsageByID(ctx, "kr")
	if err != nil {
		t.Fatalf("usage after prune: %v", err)
	}
	if after != before {
		t.Errorf("usage after prune = %d, want %d — a prune must not refund a budget", after, before)
	}

	// Both readers must agree, since the limiter refuses on one and the
	// dashboard's progress bar draws the other.
	byKey, err := repo.GetApiKeyTokenUsage(rawKey)
	if err != nil {
		t.Fatalf("usage by key after prune: %v", err)
	}
	if byKey != before {
		t.Errorf("usage by key after prune = %d, want %d", byKey, before)
	}

	// The recent row survived and the old one did not.
	if got := countRows(t, repo, "usageHistory"); got != 1 {
		t.Errorf("usageHistory rows = %d, want 1", got)
	}
}

// TestPruneUsageHistorySkipsRowsBeforeResetMark proves rows at or before the
// key's reset mark carry nothing over: they were already excluded from the
// budget, so banking them would charge the key twice.
func TestPruneUsageHistorySkipsRowsBeforeResetMark(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := t.Context()

	const rawKey = "sk-prune-reset-mark-0002"
	if err := repo.CreateApiKey("kr", rawKey, "reset key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	seedLedgerRow(t, repo, old, handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 400, 100)
	if _, err := repo.ResetApiKeyQuota(ctx, "kr"); err != nil {
		t.Fatalf("ResetApiKeyQuota: %v", err)
	}

	result, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	if result.Rows != 1 {
		t.Errorf("pruned rows = %d, want 1", result.Rows)
	}
	if result.Tokens != 0 {
		t.Errorf("banked tokens = %d, want 0 — rows before the reset mark are not spend", result.Tokens)
	}
	if got := carryoverFor(t, repo, "kr"); got != 0 {
		t.Errorf("carryover = %d, want 0", got)
	}
	if used, err := repo.GetApiKeyTokenUsageByID(ctx, "kr"); err != nil || used != 0 {
		t.Errorf("usage after prune = (%d, %v), want (0, nil)", used, err)
	}
}

// TestPruneUsageHistoryAccumulatesAndResetClears covers the two-sequence cases:
// a second prune banks on top of the first, and a reset in between wipes it.
func TestPruneUsageHistoryAccumulatesAndResetClears(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := t.Context()

	const rawKey = "sk-prune-accumulate-0003"
	if err := repo.CreateApiKey("kr", rawKey, "accumulate key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	seedLedgerRow(t, repo, time.Now().UTC().Add(-50*24*time.Hour),
		handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 100, 0)
	if _, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour, false); err != nil {
		t.Fatalf("first prune: %v", err)
	}
	if got := carryoverFor(t, repo, "kr"); got != 100 {
		t.Fatalf("carryover after first prune = %d, want 100", got)
	}

	seedLedgerRow(t, repo, time.Now().UTC().Add(-40*24*time.Hour),
		handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 60, 0)
	if _, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour, false); err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if got := carryoverFor(t, repo, "kr"); got != 160 {
		t.Fatalf("carryover after second prune = %d, want 160", got)
	}
	if used, err := repo.GetApiKeyTokenUsageByID(ctx, "kr"); err != nil || used != 160 {
		t.Fatalf("usage after two prunes = (%d, %v), want (160, nil)", used, err)
	}

	// A reset after a prune still starts from zero, which is the whole point of
	// clearing the carryover inside the reset transaction.
	if _, err := repo.ResetApiKeyQuota(ctx, "kr"); err != nil {
		t.Fatalf("ResetApiKeyQuota: %v", err)
	}
	if got := carryoverFor(t, repo, "kr"); got != 0 {
		t.Errorf("carryover after reset = %d, want 0", got)
	}
	if used, err := repo.GetApiKeyTokenUsageByID(ctx, "kr"); err != nil || used != 0 {
		t.Errorf("usage after reset = (%d, %v), want (0, nil)", used, err)
	}
}

// TestPruneUsageHistoryLeavesOtherTablesAndRecentRows pins the blast radius:
// only stale usageHistory rows go. usageDaily is the heatmap and lifetime
// totals, requestDetails is the diagnostics tab, and rows at or after the
// cutoff are still inside the retained window.
func TestPruneUsageHistoryLeavesOtherTablesAndRecentRows(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	const rawKey = "sk-prune-scope-0004"
	if err := repo.CreateApiKey("kr", rawKey, "scope key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	seedLedgerRow(t, repo, old, handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 10, 0)
	seedLedgerRow(t, repo, time.Now().UTC().Add(-1*time.Hour),
		handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 20, 0)

	seedDetail(t, repo, "detail-old", old)
	if err := repo.UpsertUsageDaily("2026-01-01", `{"requests":1}`); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
	}

	result, err := repo.PruneUsageHistory(t.Context(), 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	if result.Rows != 1 {
		t.Errorf("pruned rows = %d, want 1", result.Rows)
	}
	if got := countRows(t, repo, "usageHistory"); got != 1 {
		t.Errorf("usageHistory rows = %d, want 1 (the recent one)", got)
	}
	if got := countRows(t, repo, "usageDaily"); got != 1 {
		t.Errorf("usageDaily rows = %d, want 1 — the ledger prune must not touch the heatmap", got)
	}
	if got := countRows(t, repo, "requestDetails"); got != 1 {
		t.Errorf("requestDetails rows = %d, want 1 — the ledger prune must not touch diagnostics", got)
	}
}

// TestPruneUsageHistoryDryRunCountsWithoutDeleting pins the dry-run contract:
// the operator learns the row count before anything is removed.
func TestPruneUsageHistoryDryRunCountsWithoutDeleting(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	const rawKey = "sk-prune-dryrun-0005"
	if err := repo.CreateApiKey("kr", rawKey, "dry key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	for i := 0; i < 3; i++ {
		seedLedgerRow(t, repo, time.Now().UTC().Add(-40*24*time.Hour),
			handlerutil.MaskAPIKey(rawKey), handlerutil.MaskAPIKey(rawKey), 10, 0)
	}

	result, err := repo.PruneUsageHistory(t.Context(), 30*24*time.Hour, true)
	if err != nil {
		t.Fatalf("dry-run PruneUsageHistory: %v", err)
	}
	if !result.DryRun {
		t.Error("DryRun = false, want true")
	}
	if result.Rows != 3 {
		t.Errorf("dry-run rows = %d, want 3", result.Rows)
	}
	if got := countRows(t, repo, "usageHistory"); got != 3 {
		t.Errorf("usageHistory rows after dry run = %d, want 3 — a dry run deletes nothing", got)
	}
	if got := carryoverFor(t, repo, "kr"); got != 0 {
		t.Errorf("carryover after dry run = %d, want 0", got)
	}
}

// TestPruneUsageHistoryRejectsNonPositiveRetention keeps an accidental 0 from
// being read as "delete the whole ledger".
func TestPruneUsageHistoryRejectsNonPositiveRetention(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	for _, window := range []time.Duration{0, -time.Hour, 12 * time.Hour} {
		if _, err := repo.PruneUsageHistory(context.Background(), window, false); err == nil {
			t.Errorf("PruneUsageHistory(%v) = nil error, want a refusal", window)
		}
	}
}

// TestUsageHistoryRetentionDefaultsToOff proves the setting is opt-in: an
// install that has never touched it keeps every row.
func TestUsageHistoryRetentionDefaultsToOff(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	if got := repo.UsageHistoryRetention(); got != 0 {
		t.Errorf("default UsageHistoryRetention = %v, want 0 (keep forever)", got)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"usageHistoryRetentionDays": 14}); err != nil {
		t.Fatalf("UpdateSettingsRaw: %v", err)
	}
	if got := repo.UsageHistoryRetention(); got != 14*24*time.Hour {
		t.Errorf("UsageHistoryRetention = %v, want %v", got, 14*24*time.Hour)
	}
}
