package db

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/vault"
)

// TestVaultNestedProviderSpecificData verifies that when Vault is enabled,
// saving connection data containing nested providerSpecificData credentials redacts them
// from the raw JSON column at rest, and hydration restores them to plaintext.
func TestVaultNestedProviderSpecificData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}

	repo := NewRepo(database)
	repo.SetVault(v)

	connID := "conn-nested-1"
	data := `{"baseUrl":"https://example.invalid/v1","providerSpecificData":{"qwenCloudCookie":"cookie-abc-123","consoleApiKey":"key-xyz-789","otherConfig":"keep-me"}}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'qwen', 'cookie', 'test', 1, 1, ?, ?, ?)`,
		connID, data, now, now,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	// Trigger seal on write or migration
	if _, err := repo.MigratePlaintextCredentials(path); err != nil {
		t.Fatalf("migrate plaintext credentials: %v", err)
	}

	// Verify raw data at rest no longer contains the sensitive cookies/keys
	raw := rawConnectionData(t, repo, connID)
	if strings.Contains(raw, "cookie-abc-123") {
		t.Errorf("raw data at rest still contains qwenCloudCookie: %s", raw)
	}
	if strings.Contains(raw, "key-xyz-789") {
		t.Errorf("raw data at rest still contains consoleApiKey: %s", raw)
	}
	if !strings.Contains(raw, "keep-me") {
		t.Errorf("raw data at rest lost non-secret providerSpecificData config: %s", raw)
	}

	// Verify hydration restores the nested credentials correctly
	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}
	if !strings.Contains(conn.Data, "cookie-abc-123") {
		t.Errorf("hydrated data missing qwenCloudCookie: %s", conn.Data)
	}
	if !strings.Contains(conn.Data, "key-xyz-789") {
		t.Errorf("hydrated data missing consoleApiKey: %s", conn.Data)
	}
}

// TestVaultDisabledNestedProviderSpecificData verifies that unvaulted setup leaves nested credentials untouched.
func TestVaultDisabledNestedProviderSpecificData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	repo := NewRepo(database)
	// Vault is not set (disabled)

	connID := "conn-nested-2"
	data := `{"baseUrl":"https://example.invalid/v1","providerSpecificData":{"qwenCloudCookie":"cookie-abc-123"}}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'qwen', 'cookie', 'test', 1, 1, ?, ?, ?)`,
		connID, data, now, now,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	// Migrate with no vault attached should be a no-op
	if _, err := repo.MigratePlaintextCredentials(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	raw := rawConnectionData(t, repo, connID)
	if !strings.Contains(raw, "cookie-abc-123") {
		t.Errorf("unvaulted setup altered raw data: %s", raw)
	}
}
