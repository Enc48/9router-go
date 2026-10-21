package chat

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// seedKeyWithPlan inserts an apiKeys row and a plan, then assigns the plan.
func seedKeyWithPlan(t *testing.T, sqlDB *sql.DB, repo *db.Repo, keyID, planID string, allowed []string) {
	t.Helper()
	if _, err := sqlDB.Exec(`INSERT INTO apiKeys (id, key, name, createdAt) VALUES (?, ?, ?, '2026-07-18T00:00:00Z')`,
		keyID, "sk-"+keyID, keyID); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	if err := repo.CreatePlan(db.Plan{ID: planID, TenantID: "t1", Name: planID, Period: db.PlanPeriodMonthly, AllowedModels: allowed}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := repo.SetApiKeyPlan(keyID, planID); err != nil {
		t.Fatalf("assign plan: %v", err)
	}
}

// TestPlanAllowlistFailsClosed is the guard #476 asks for: a key whose access
// comes from its PLAN must not become unrestricted when the plan cannot be read.
func TestPlanAllowlistFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		breakPlan  func(t *testing.T, sqlDB *sql.DB)
		wantPolicy bool // true => the loader fails closed with ErrPolicyUnavailable
	}{
		{
			name: "intact plan restricts",
		},
		{
			name: "plan table unreadable",
			breakPlan: func(t *testing.T, sqlDB *sql.DB) {
				t.Helper()
				if _, err := sqlDB.Exec(`DROP TABLE plans`); err != nil {
					t.Fatalf("drop plans: %v", err)
				}
			},
			wantPolicy: true,
		},
		{
			name: "dangling plan id",
			breakPlan: func(t *testing.T, sqlDB *sql.DB) {
				t.Helper()
				if _, err := sqlDB.Exec(`DELETE FROM plans WHERE id = 'p1'`); err != nil {
					t.Fatalf("delete plan: %v", err)
				}
			},
			wantPolicy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlDB, repo, cleanup := setupFailClosedTestDB(t)
			defer cleanup()
			h := NewChatHandler(repo)
			seedKeyWithPlan(t, sqlDB, repo, "k-plan", "p1", []string{"gpt-4o"})
			key := &models.APIKey{ID: "k-plan", PlanID: strPtr("p1")}

			if tt.breakPlan != nil {
				tt.breakPlan(t, sqlDB)
			}

			err := h.checkModelAccess(key, "claude-3-opus")
			if tt.wantPolicy {
				if err == nil || !IsPolicyUnavailable(err) {
					t.Fatalf("checkModelAccess = %v, want ErrPolicyUnavailable", err)
				}
				if IsModelNotAllowed(err) {
					t.Fatalf("a broken plan read must not read as an access denial: %v", err)
				}
				rec := httptest.NewRecorder()
				writeModelAccessError(rec, err)
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("client status = %d, want 503", rec.Code)
				}
				// The listing loader and the capacity-adapter loader must deny too.
				if got := h.allowedModelPatterns("k-plan"); len(got) != 1 || got[0] != SentinelDenyAll {
					t.Fatalf("allowedModelPatterns = %v, want [%s]", got, SentinelDenyAll)
				}
				if modelAllowed(h.allowedModelPatterns("k-plan"), "any-random-model") {
					t.Fatal("capacity adapter would inject a model the key is not entitled to")
				}
				return
			}

			// Intact plan: the plan's grant is enforced in both directions.
			if err == nil || !IsModelNotAllowed(err) {
				t.Fatalf("denied model = %v, want ErrModelNotAllowed", err)
			}
			if err := h.checkModelAccess(key, "gpt-4o"); err != nil {
				t.Fatalf("granted model refused: %v", err)
			}
			if modelAllowed(h.allowedModelPatterns("k-plan"), "claude-3-opus") {
				t.Fatal("capacity adapter saw the denied model as permitted")
			}
			if !modelAllowed(h.allowedModelPatterns("k-plan"), "gpt-4o") {
				t.Fatal("capacity adapter refused the plan's own grant")
			}
		})
	}
}

// TestPlanPatternsKeepUnionSemantics pins what must NOT change: a key without a
// plan is untouched, a plan only ever widens, and a deny-only plan keeps denying.
func TestPlanPatternsKeepUnionSemantics(t *testing.T) {
	tests := []struct {
		name     string
		own      []string
		plan     []string
		hasPlan  bool
		wantPats []string
	}{
		{"no plan, no own rows", nil, nil, false, nil},
		{"plan only", nil, []string{"gpt-4o"}, true, []string{"gpt-4o"}},
		{"own only", []string{"oc/*"}, nil, false, []string{"oc/*"}},
		{"plan widens own", []string{"oc/*"}, []string{"gpt-4o"}, true, []string{"oc/*", "gpt-4o"}},
		{"deny-only plan", nil, []string{"!gpt-4o"}, true, []string{"!gpt-4o"}},
		{"empty plan keeps own", []string{"oc/*"}, nil, true, []string{"oc/*"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlDB, repo, cleanup := setupFailClosedTestDB(t)
			defer cleanup()
			h := NewChatHandler(repo)
			if _, err := sqlDB.Exec(`INSERT INTO apiKeys (id, key, name, createdAt) VALUES ('k1', 'sk-k1', 'k1', '2026-07-18T00:00:00Z')`); err != nil {
				t.Fatalf("insert key: %v", err)
			}
			planID := ""
			if tt.hasPlan {
				if err := repo.CreatePlan(db.Plan{ID: "p1", TenantID: "t1", Name: "p1", Period: db.PlanPeriodMonthly, AllowedModels: tt.plan}); err != nil {
					t.Fatalf("create plan: %v", err)
				}
				if err := repo.SetApiKeyPlan("k1", "p1"); err != nil {
					t.Fatalf("assign plan: %v", err)
				}
				planID = "p1"
			}
			got, err := h.withPlanPatterns("k1", &planID, tt.own)
			if err != nil {
				t.Fatalf("withPlanPatterns: %v", err)
			}
			if len(got) != len(tt.wantPats) {
				t.Fatalf("patterns = %v, want %v", got, tt.wantPats)
			}
			for i := range got {
				if got[i] != tt.wantPats[i] {
					t.Fatalf("patterns = %v, want %v", got, tt.wantPats)
				}
			}
		})
	}
}

// TestPlanReadFailureIsNotAnAccessDenial keeps the two typed outcomes apart: a
// broken policy read is a 503, never a 403.
func TestPlanReadFailureIsNotAnAccessDenial(t *testing.T) {
	sqlDB, repo, cleanup := setupFailClosedTestDB(t)
	defer cleanup()
	h := NewChatHandler(repo)
	seedKeyWithPlan(t, sqlDB, repo, "k-plan", "p1", []string{"gpt-4o"})
	if _, err := sqlDB.Exec(`DROP TABLE plans`); err != nil {
		t.Fatalf("drop plans: %v", err)
	}
	_, err := h.effectiveAllowedModels(&models.APIKey{ID: "k-plan", PlanID: strPtr("p1")})
	if err == nil {
		t.Fatal("expected a fail-closed error")
	}
	if !IsPolicyUnavailable(err) || IsModelNotAllowed(err) {
		t.Fatalf("error = %v, want ErrPolicyUnavailable and not ErrModelNotAllowed", err)
	}
}

// TestPlanPatternsByID treats a missing plan row as a failure, not as "no plan".
func TestPlanPatternsByID(t *testing.T) {
	sqlDB, repo, cleanup := setupFailClosedTestDB(t)
	defer cleanup()
	h := NewChatHandler(repo)
	seedKeyWithPlan(t, sqlDB, repo, "k-plan", "p1", []string{"gpt-4o"})

	if got, err := h.planPatternsByID("k-plan", "p1"); err != nil || len(got) != 1 || got[0] != "gpt-4o" {
		t.Fatalf("intact plan = %v, %v", got, err)
	}
	if got, err := h.planPatternsByID("k-plan", ""); err != nil || got != nil {
		t.Fatalf("empty plan id = %v, %v (want no plan, no error)", got, err)
	}
	if _, err := sqlDB.Exec(`DELETE FROM plans WHERE id = 'p1'`); err != nil {
		t.Fatalf("delete plan: %v", err)
	}
	if _, err := h.planPatternsByID("k-plan", "p1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("dangling plan id error = %v, want one wrapping sql.ErrNoRows", err)
	}
	// The key-id path detects the same dangling plan by reading the key's planId,
	// because the join alone cannot tell "no plan" from "plan that is not there".
	if _, err := h.planPatternsByKey("k-plan"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("planPatternsByKey dangling error = %v, want one wrapping sql.ErrNoRows", err)
	}
	// A key that genuinely has no plan is untouched.
	if _, err := sqlDB.Exec(`UPDATE apiKeys SET planId = '' WHERE id = 'k-plan'`); err != nil {
		t.Fatalf("clear plan: %v", err)
	}
	if got, err := h.planPatternsByKey("k-plan"); err != nil || got != nil {
		t.Fatalf("no-plan key = %v, %v (want no plan, no error)", got, err)
	}
	// A key id with no row behind it has no policy to evaluate either, and the
	// repo reports that as a nil key rather than an error.
	if got, err := h.planPatternsByKey("no-such-key"); err != nil || got != nil {
		t.Fatalf("unknown key = %v, %v (want no plan, no error)", got, err)
	}
}

func strPtr(s string) *string { return &s }
