package config

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/argon2"
)

const (
	// Master key derivation parameters.
	keySalt      = "code-warden-credential-store-v1"
	argonTime    = 3
	argonMem     = 64 * 1024 // 64 MB
	argonThreads = 4
	argonKeyLen  = 32 // AES-256
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
// deterministically from the machine's identity so credentials survive restarts.
func NewCredentialStore(db *sqlx.DB) (*CredentialStore, error) {
	machineID := getMachineID()
	masterKey := deriveMasterKey(machineID)

	// Verify the key works by attempting to encrypt/decrypt a test value.
	if err := verifyKey(masterKey); err != nil {
		return nil, fmt.Errorf("credential store key verification failed: %w", err)
	}

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

	query := `INSERT INTO credentials (id, data) VALUES ($1, $2)
	          ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()`
	_, err = cs.db.ExecContext(ctx, query, id, encrypted)
	if err != nil {
		return fmt.Errorf("failed to store credential %s: %w", id, err)
	}
	return nil
}

// Load retrieves and decrypts a credential blob. Returns false if not found.
func (cs *CredentialStore) Load(ctx context.Context, id string, dest any) (bool, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var encrypted []byte
	query := `SELECT data FROM credentials WHERE id = $1`
	err := cs.db.GetContext(ctx, &encrypted, query, id)
	if err != nil {
		return false, nil // not found
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

// HasCredentials returns true if the given credential ID exists.
func (cs *CredentialStore) HasCredentials(ctx context.Context, id string) bool {
	var exists bool
	_ = cs.db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM credentials WHERE id = $1)`, id)
	return exists
}

func getMachineID() string {
	// Try Linux machine-id first.
	if id, err := os.ReadFile("/etc/machine-id"); err == nil && len(id) > 0 {
		return string(id)
	}
	// Fall back to hostname.
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "code-warden-default"
	}
	return hostname
}

func deriveMasterKey(machineID string) []byte {
	salt := sha256.Sum256([]byte(keySalt))
	return argon2.IDKey([]byte(machineID), salt[:], argonTime, argonMem, argonThreads, argonKeyLen)
}

func verifyKey(key []byte) error {
	testData := []byte("verification")
	encrypted, err := encrypt(key, testData)
	if err != nil {
		return err
	}
	decrypted, err := decrypt(key, encrypted)
	if err != nil {
		return err
	}
	if string(decrypted) != string(testData) {
		return fmt.Errorf("round-trip verification failed")
	}
	return nil
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
