package config

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/argon2"
)

const (
	// Master key derivation parameters. Kept moderate — the "password" (machine
	// ID) isn't secret, so Argon2 is mainly a slow-down, not a brute-force
	// defence. Reducing time/memory makes startup snappier on small machines.
	keySalt      = "code-warden-credential-store-v1"
	argonTime    = 1
	argonMem     = 16 * 1024 // 16 MB
	argonThreads = 2
	argonKeyLen  = 32 // AES-256

	// masterKeyFile is the persisted random key fallback. When neither a stable
	// machine ID nor a Windows MachineGuid is available, we generate a random
	// key and store it here so credentials survive restarts.
	masterKeyFile = "credentials.key"
)

// GitHubAppCredentials holds the GitHub App configuration.
type GitHubAppCredentials struct {
	AppID          int64  `json:"app_id"`
	WebhookSecret  string `json:"webhook_secret"`
	PrivateKeyPEM  string `json:"private_key_pem"`
	AppName        string `json:"app_name,omitempty"`
	InstallationID int64  `json:"installation_id,omitempty"`
}

// LLMCredentials holds LLM provider credentials.
type LLMCredentials struct {
	Provider     string `json:"provider"`
	GeminiAPIKey string `json:"gemini_api_key,omitempty"`
	OllamaAPIKey string `json:"ollama_api_key,omitempty"`
}

// CredentialStore provides encrypted credential storage backed by PostgreSQL.
type CredentialStore struct {
	db        *sqlx.DB
	masterKey []byte
	mu        sync.RWMutex
}

// NewCredentialStore creates a CredentialStore. The master key is derived
// from a stable machine identity (Linux /etc/machine-id, Windows MachineGuid,
// or a persisted random key file as a last resort) so credentials survive
// restarts and container recreation.
func NewCredentialStore(db *sqlx.DB) (*CredentialStore, error) {
	machineID, err := stableMachineID()
	if err != nil {
		return nil, fmt.Errorf("failed to determine stable machine identity: %w", err)
	}
	masterKey := deriveMasterKey(machineID)

	return &CredentialStore{
		db:        db,
		masterKey: masterKey,
	}, nil
}

// Save encrypts and stores a credential blob under the given ID.
func (cs *CredentialStore) Save(ctx context.Context, id string, data any) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal credential %s: %w", id, err)
	}

	encrypted, err := encrypt(cs.masterKey, jsonBytes)
	if err != nil {
		return fmt.Errorf("failed to encrypt credential %s: %w", id, err)
	}

	// Rebind to the driver's placeholder dialect ($1 for Postgres, ? for
	// SQLite/MySQL) so the credential store works under any sqlx driver.
	// CURRENT_TIMESTAMP is portable (Postgres + SQLite); the updated_at
	// trigger from migration 000014 keeps updated_at in sync on Postgres.
	query := cs.db.Rebind(`INSERT INTO credentials (id, data) VALUES ($1, $2)
	          ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = CURRENT_TIMESTAMP`)
	_, err = cs.db.ExecContext(ctx, query, id, encrypted)
	if err != nil {
		return fmt.Errorf("failed to store credential %s: %w", id, err)
	}
	return nil
}

// Load retrieves and decrypts a credential blob. Returns (false, nil) when the
// credential is not stored; (false, err) for transient DB or decryption errors.
func (cs *CredentialStore) Load(ctx context.Context, id string, dest any) (bool, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var encrypted []byte
	query := `SELECT data FROM credentials WHERE id = $1`
	err := cs.db.GetContext(ctx, &encrypted, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // genuinely not configured yet
		}
		return false, fmt.Errorf("failed to read credential %s: %w", id, err)
	}

	decrypted, err := decrypt(cs.masterKey, encrypted)
	if err != nil {
		return false, fmt.Errorf("failed to decrypt credential %s: %w", id, err)
	}

	if err := json.Unmarshal(decrypted, dest); err != nil {
		return false, fmt.Errorf("failed to unmarshal credential %s: %w", id, err)
	}
	return true, nil
}

// Delete removes a credential by ID.
func (cs *CredentialStore) Delete(ctx context.Context, id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	_, err := cs.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = $1`, id)
	return err
}

// HasCredentials returns true if the given credential ID exists. A transient
// DB error is treated as "not present" — callers that need to distinguish
// should use Load and inspect the error.
func (cs *CredentialStore) HasCredentials(ctx context.Context, id string) bool {
	var exists bool
	err := cs.db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM credentials WHERE id = $1)`, id)
	return err == nil && exists
}

// stableMachineID returns a value that stays the same across reboots and
// container restarts on the same host. Order of preference:
//  1. Persisted random key file (always wins if present — works everywhere).
//  2. Linux /etc/machine-id (stable across container restarts when /etc is
//     mounted from a volume or the host).
//  3. Windows MachineGuid from the registry (stable across reboots/rename).
//  4. os.Hostname() as a final fallback (with a warning printed by the caller).
//
// If none of the stable sources is available, we generate a random 32-byte key
// and persist it to <dataDir>/credentials.key so subsequent runs reuse it.
func stableMachineID() (string, error) {
	// 1. Persisted random key file.
	if keyFile := masterKeyPath(); keyFile != "" {
		if data, err := os.ReadFile(keyFile); err == nil && len(data) > 0 {
			return string(data), nil
		}
	}

	// 2. Linux machine-id.
	if id, err := os.ReadFile("/etc/machine-id"); err == nil && len(id) > 0 {
		return string(id), nil
	}

	// 3. Windows MachineGuid. windowsMachineGUID returns ("", nil) on
	// non-Windows platforms, so this branch is effectively a no-op elsewhere.
	if guid, err := windowsMachineGUID(); err == nil && guid != "" {
		return guid, nil
	}

	// 4. Persist a random key as the last resort.
	keyFile := masterKeyPath()
	if keyFile == "" {
		// No writable data dir — fall back to hostname (least stable). We
		// deliberately ignore the error: hostname() failing is extremely rare
		// and we still need *something* to key on, so use a fixed fallback.
		hostname, hostErr := os.Hostname()
		if hostErr != nil || hostname == "" {
			return "code-warden-default", nil //nolint:nilerr // intentional fallback when hostname is unavailable
		}
		return hostname, nil
	}

	random := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", fmt.Errorf("failed to generate master key: %w", err)
	}
	encoded := hex.EncodeToString(random)
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return "", fmt.Errorf("failed to create master key directory: %w", err)
	}
	if err := os.WriteFile(keyFile, []byte(encoded), 0o600); err != nil {
		return "", fmt.Errorf("failed to persist master key: %w", err)
	}
	return encoded, nil
}

// masterKeyPath returns the path where a random master key may be persisted,
// or "" if no writable data directory is configured.
func masterKeyPath() string {
	dir := os.Getenv("CODE_WARDEN_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	return filepath.Join(dir, masterKeyFile)
}

func deriveMasterKey(machineID string) []byte {
	salt := sha256.Sum256([]byte(keySalt))
	return argon2.IDKey([]byte(machineID), salt[:], argonTime, argonMem, argonThreads, argonKeyLen)
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
