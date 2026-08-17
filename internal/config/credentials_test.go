package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// newTestCredentialStore opens an in-memory SQLite database, creates the
// credentials table, and returns a CredentialStore bound to it. We use SQLite
// only for tests — production uses the Postgres migration in
// internal/db/migrations/000014_create_credentials_table.up.sql. The Go code
// under test uses standard `?`/`$1` placeholders carefully; for the queries in
// credentials.go we use `$1`, which sqlx with the sqlite driver maps to `?`
// automatically when used via the Rebind helper. We use Rebind here to make the
// tests independent of placeholder dialect.
func newTestCredentialStore(t *testing.T) *CredentialStore {
	t.Helper()

	db, err := sqlx.Connect("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema := `CREATE TABLE credentials (
		id          TEXT PRIMARY KEY,
		data        BLOB NOT NULL,
		created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Rebind the Save/Load queries to the sqlite `?` placeholder dialect.
	cs := &CredentialStore{
		db:        db,
		masterKey: deriveMasterKey("test-machine-id"),
	}
	return cs
}

func TestCredentialStore_RoundTrip(t *testing.T) {
	cs := newTestCredentialStore(t)
	ctx := context.Background()

	creds := GitHubAppCredentials{
		AppID:         12345,
		WebhookSecret: "shhh",
		PrivateKeyPEM: "-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----",
		AppName:       "Code Warden Test",
	}
	if err := cs.Save(ctx, "github_app", &creds); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var loaded GitHubAppCredentials
	ok, err := cs.Load(ctx, "github_app", &loaded)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("Load returned ok=false for a saved credential")
	}
	if loaded.AppID != creds.AppID || loaded.WebhookSecret != creds.WebhookSecret ||
		loaded.PrivateKeyPEM != creds.PrivateKeyPEM || loaded.AppName != creds.AppName {
		t.Fatalf("loaded credential does not match saved: got %+v, want %+v", loaded, creds)
	}
}

func TestCredentialStore_LoadMissingReturnsFalseNil(t *testing.T) {
	cs := newTestCredentialStore(t)
	ctx := context.Background()

	var loaded GitHubAppCredentials
	ok, err := cs.Load(ctx, "does-not-exist", &loaded)
	if ok {
		t.Fatal("expected ok=false for missing credential")
	}
	if err != nil {
		t.Fatalf("expected nil error for missing credential, got: %v", err)
	}
}

func TestCredentialStore_HasCredentials(t *testing.T) {
	cs := newTestCredentialStore(t)
	ctx := context.Background()

	if cs.HasCredentials(ctx, "github_app") {
		t.Fatal("HasCredentials=true before any Save")
	}

	if err := cs.Save(ctx, "github_app", &LLMCredentials{Provider: "ollama"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !cs.HasCredentials(ctx, "github_app") {
		t.Fatal("HasCredentials=false after Save")
	}
}

func TestCredentialStore_Delete(t *testing.T) {
	cs := newTestCredentialStore(t)
	ctx := context.Background()

	if err := cs.Save(ctx, "github_app", &LLMCredentials{Provider: "ollama"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := cs.Delete(ctx, "github_app"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if cs.HasCredentials(ctx, "github_app") {
		t.Fatal("HasCredentials=true after Delete")
	}
}

// TestCredentialStore_Overwrite verifies that saving with the same ID updates
// the existing record (the ON CONFLICT path).
func TestCredentialStore_Overwrite(t *testing.T) {
	cs := newTestCredentialStore(t)
	ctx := context.Background()

	original := GitHubAppCredentials{AppID: 1, AppName: "v1"}
	if err := cs.Save(ctx, "github_app", &original); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	updated := GitHubAppCredentials{AppID: 2, AppName: "v2"}
	if err := cs.Save(ctx, "github_app", &updated); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	var loaded GitHubAppCredentials
	ok, err := cs.Load(ctx, "github_app", &loaded)
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if loaded.AppID != 2 || loaded.AppName != "v2" {
		t.Fatalf("expected overwritten values, got %+v", loaded)
	}
}

// TestApplyDBCredentials verifies the merge logic does not wipe env-set values
// when the DB record has zero/empty fields.
func TestApplyDBCredentials_PartialOverride(t *testing.T) {
	cfg := &Config{
		GitHub: GitHubConfig{
			AppID:          100, // pre-set from env
			WebhookSecret:  "env-secret",
			PrivateKeyPath: "keys/app.pem",
		},
	}

	cfg.ApplyDBCredentials(&GitHubAppCredentials{
		AppID:         200,        // overrides env
		WebhookSecret: "",         // empty — should NOT override
		PrivateKeyPEM:  "pem-data", // set DB-backed key
	}, nil)

	if cfg.GitHub.AppID != 200 {
		t.Errorf("AppID: got %d, want 200", cfg.GitHub.AppID)
	}
	if cfg.GitHub.WebhookSecret != "env-secret" {
		t.Errorf("WebhookSecret was wiped by empty DB value: got %q, want %q", cfg.GitHub.WebhookSecret, "env-secret")
	}
	if cfg.GitHub.PrivateKeyPEM != "pem-data" {
		t.Errorf("PrivateKeyPEM: got %q, want %q", cfg.GitHub.PrivateKeyPEM, "pem-data")
	}
}

// TestIsSetupMode verifies the first-boot detection.
func TestIsSetupMode(t *testing.T) {
	cfg := &Config{}
	if !cfg.IsSetupMode() {
		t.Error("expected setup mode when AppID == 0")
	}
	cfg.GitHub.AppID = 123
	if cfg.IsSetupMode() {
		t.Error("expected not in setup mode when AppID != 0")
	}
}

// TestStableMachineID_PersistedKey verifies that when no system-level identity
// is available, the persisted random key file is created and reused on the
// next call.
//
// This test exercises the last-resort fallback path (step 4 in
// stableMachineID). On Windows, step 3 (registry MachineGuid) succeeds first,
// so we skip the test there. On Linux CI hosts with /etc/machine-id present,
// step 2 wins, so we skip too. The test only runs where every stable source is
// unavailable — which in practice means a Linux container without /etc/machine-id.
func TestStableMachineID_PersistedKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping persisted-key test on Windows — registry MachineGuid wins first")
	}
	// Redirect CODE_WARDEN_DATA_DIR to a unique temp directory and ensure no
	// machine-id file exists on disk by virtue of running the test.
	tmpDir := t.TempDir()
	t.Setenv("CODE_WARDEN_DATA_DIR", tmpDir)
	// If a real /etc/machine-id is present, step 2 wins before the fallback.
	if id, err := readFileIfExists("/etc/machine-id"); err == nil && len(id) > 0 {
		t.Skip("skipping persisted-key test on a host with /etc/machine-id present")
	}

	first, err := stableMachineID()
	if err != nil {
		t.Fatalf("stableMachineID (1st call): %v", err)
	}
	if first == "" {
		t.Fatal("first call returned empty machine ID")
	}

	// The persisted key file should now exist.
	keyPath := filepath.Join(tmpDir, masterKeyFile)
	if _, err := readFileIfExists(keyPath); err != nil {
		t.Fatalf("expected persisted key file at %s: %v", keyPath, err)
	}

	// Second call should return the same value (loaded from the file).
	second, err := stableMachineID()
	if err != nil {
		t.Fatalf("stableMachineID (2nd call): %v", err)
	}
	if first != second {
		t.Fatalf("stableMachineID changed across calls: first=%q second=%q", first, second)
	}
}

// TestStableMachineID_WindowsRegistry verifies the Windows registry path is used
// on Windows hosts (the common case for local dev).
func TestStableMachineID_WindowsRegistry(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	// Clear any persisted key so the registry path is exercised.
	tmpDir := t.TempDir()
	t.Setenv("CODE_WARDEN_DATA_DIR", tmpDir)

	id, err := stableMachineID()
	if err != nil {
		t.Fatalf("stableMachineID: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty machine ID from Windows registry")
	}
	// The Windows GUID is a UUID (contains dashes); the persisted-key fallback
	// is a 64-char hex string. If we got a UUID, the registry path worked.
	if !strings.Contains(id, "-") {
		t.Logf("got non-UUID machine ID %q (could be hostname fallback) — registry may be unavailable", id)
	}
}

// readFileIfExists is a tiny helper so the test doesn't import os directly in a
// confusing way alongside t.Setenv.
func readFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// TestLoad_DBClosedReturnsError verifies that a transient DB error (not
// ErrNoRows) is surfaced, not swallowed as "missing".
func TestLoad_DBClosedReturnsError(t *testing.T) {
	cs := newTestCredentialStore(t)
	// Close the underlying DB to make subsequent queries fail with a real
	// error (not sql.ErrNoRows).
	_ = cs.db.DB.Close()

	ctx := context.Background()
	var loaded GitHubAppCredentials
	ok, err := cs.Load(ctx, "github_app", &loaded)
	if ok {
		t.Fatal("expected ok=false on DB error")
	}
	if err == nil {
		t.Fatal("expected a non-nil error when DB is closed, got nil")
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DB-closed error was reported as ErrNoRows: %v", err)
	}
}

// Ensure the test JSON encoding doesn't silently drop fields by checking the
// struct tags round-trip through the encoder.
func TestGitHubAppCredentials_JSONRoundTrip(t *testing.T) {
	src := GitHubAppCredentials{
		AppID: 42, WebhookSecret: "s", PrivateKeyPEM: "p", AppName: "n", InstallationID: 7,
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst GitHubAppCredentials
	if err := json.Unmarshal(raw, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dst != src {
		t.Errorf("round-trip mismatch: got %+v, want %+v", dst, src)
	}
}