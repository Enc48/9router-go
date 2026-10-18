package dashboard

import (
	"net/http"
	"strconv"
	"time"

	"9router/proxy/internal/handlerutil"
)

// HandleGetDatabaseStats handles GET /api/settings/database/stats.
func (h *DashboardHandler) HandleGetDatabaseStats(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}
	stats, err := h.Repo.GetDatabaseStats(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to get database stats: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, stats)
}

// HandlePruneDatabase handles POST /api/settings/database/prune.
func (h *DashboardHandler) HandlePruneDatabase(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}
	retention := 7 * 24 * time.Hour
	if settings, err := h.Repo.GetSettings(); err == nil && settings != nil && settings.RetentionDays > 0 {
		retention = time.Duration(settings.RetentionDays) * 24 * time.Hour
	}
	result, err := h.Repo.PruneRequestDetails(r.Context(), retention)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to prune database: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"pruned":  result.Total(),
		"byAge":   result.ByAge,
		"bySize":  result.BySize,
	})
}

// HandlePruneUsageHistory handles POST /api/settings/database/prune-usage.
//
// Separate from /prune on purpose: that button owns requestDetails, the
// diagnostics table, and an operator pressing it must never delete billing
// rows as a side effect. This one is the only path that touches usageHistory,
// and it takes an explicit window plus an explicit dry-run so the row count is
// known before anything goes.
func (h *DashboardHandler) HandlePruneUsageHistory(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}

	dryRun := r.URL.Query().Get("dryRun") == "1" || r.URL.Query().Get("dryRun") == "true"

	days := h.Repo.UsageHistoryRetention()
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "days must be a positive integer")
			return
		}
		days = time.Duration(n) * 24 * time.Hour
	}
	if days <= 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"usageHistory retention is not configured; set usageHistoryRetentionDays or pass ?days=N")
		return
	}

	result, err := h.Repo.PruneUsageHistory(r.Context(), days, dryRun)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to prune usage history: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"dryRun":  result.DryRun,
		"rows":    result.Rows,
		"tokens":  result.Tokens,
		"cutoff":  result.Cutoff,
		"days":    int(days / (24 * time.Hour)),
		"pruned":  result.Rows,
		"ledger":  true,
	})
}

// HandleVacuumDatabase handles POST /api/settings/database/vacuum.
func (h *DashboardHandler) HandleVacuumDatabase(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}
	if err := h.Repo.VacuumDatabase(r.Context()); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to vacuum database: "+err.Error())
		return
	}
	stats, _ := h.Repo.GetDatabaseStats(r.Context())
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"databaseSizeBytes": stats.DatabaseSizeBytes,
		"reclaimableBytes":  stats.ReclaimableBytes,
	})
}
