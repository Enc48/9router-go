package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The ledger prune is a separate endpoint from /prune on purpose (#392): the
// existing button owns requestDetails, and pressing it must never delete
// billing rows. These tests pin the endpoint's contract — dry run first, then
// an explicit window, and a refusal when neither is supplied.

func pruneUsageFixture(t *testing.T) (http.Handler, func()) {
	t.Helper()
	repo, cleanup := setupTestDB(t)

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS usageHistory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			apiKey TEXT,
			clientKey TEXT,
			endpoint TEXT,
			promptTokens INTEGER,
			completionTokens INTEGER,
			cost REAL,
			status TEXT,
			tokens TEXT,
			meta TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS usageLedgerCarryover (
			keyId TEXT PRIMARY KEY,
			tokens INTEGER NOT NULL DEFAULT 0,
			updatedAt TEXT NOT NULL DEFAULT ''
		);`,
	}
	for _, q := range stmts {
		if _, err := repo.RawDB().Exec(q); err != nil {
			cleanup()
			t.Fatalf("create fixture table: %v", err)
		}
	}

	seed := func(at time.Time, prompt int) {
		t.Helper()
		if _, err := repo.RawDB().Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey,
			                           endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
			 VALUES (?, 'openai', 'gpt-4o', 'c1', 'sk-masked', 'sk-masked',
			         '/v1/chat/completions', ?, 0, 0, 'success', '{}', '{}')`,
			at.UTC().Format(time.RFC3339), prompt,
		); err != nil {
			cleanup()
			t.Fatalf("seed usageHistory: %v", err)
		}
	}
	seed(time.Now().UTC().Add(-40*24*time.Hour), 100)
	seed(time.Now().UTC().Add(-time.Hour), 20)

	return setupTestRouter(repo), cleanup
}

func TestHandlePruneUsageHistory_DryRunThenPrune(t *testing.T) {
	router, cleanup := pruneUsageFixture(t)
	defer cleanup()

	// Dry run: reports the count, deletes nothing.
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database/prune-usage?days=30&dryRun=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var dry map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dry); err != nil {
		t.Fatalf("decode dry run: %v", err)
	}
	if got, _ := dry["rows"].(float64); got != 1 {
		t.Errorf("dry run rows = %v, want 1", dry["rows"])
	}
	if dry["dryRun"] != true {
		t.Errorf("dryRun = %v, want true", dry["dryRun"])
	}

	// Real prune with the same window.
	req = httptest.NewRequest(http.MethodPost, "/api/settings/database/prune-usage?days=30", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("prune status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var done map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &done); err != nil {
		t.Fatalf("decode prune: %v", err)
	}
	if got, _ := done["rows"].(float64); got != 1 {
		t.Errorf("pruned rows = %v, want 1", done["rows"])
	}
	if done["dryRun"] != false {
		t.Errorf("dryRun = %v, want false", done["dryRun"])
	}
}

func TestHandlePruneUsageHistory_RefusesWithoutWindow(t *testing.T) {
	router, cleanup := pruneUsageFixture(t)
	defer cleanup()

	// No ?days and no usageHistoryRetentionDays setting: the endpoint must not
	// guess a window and delete billing rows.
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database/prune-usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/settings/database/prune-usage?days=0", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("days=0 status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
