package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "runic"
)

var ErrNoStoredToken = errors.New("no stored oauth token")

type StoredToken struct {
	Provider     string    `json:"provider"`
	AccountID    string    `json:"account_id"`
	Login        string    `json:"login"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	SavedAt      time.Time `json:"saved_at"`
}

func SaveStoredToken(t StoredToken) error {
	t.Provider = strings.ToLower(strings.TrimSpace(t.Provider))
	if t.Provider == "" {
		return errors.New("provider is required")
	}
	if strings.TrimSpace(t.AccessToken) == "" {
		return errors.New("access token is required")
	}
	t.SavedAt = time.Now().UTC()

	b, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal stored token: %w", err)
	}
	if err := keyring.Set(keyringService, keyringKey(t.Provider), string(b)); err != nil {
		return fmt.Errorf("save token to keychain: %w", err)
	}

	// Store non-sensitive metadata separately for easy status without exposing secrets.
	meta := map[string]any{
		"provider":   t.Provider,
		"account_id": t.AccountID,
		"login":      t.Login,
		"name":       t.Name,
		"email":      t.Email,
		"scope":      t.Scope,
		"expires_at": t.ExpiresAt,
		"saved_at":   t.SavedAt,
	}
	if err := writeMetadataFile(t.Provider, meta); err != nil {
		return err
	}
	return nil
}

func LoadStoredToken(provider string) (*StoredToken, error) {
	provider = normalizeProvider(provider)
	if provider == "" {
		return nil, errors.New("provider is required")
	}
	v, err := keyring.Get(keyringService, keyringKey(provider))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrNoStoredToken
		}
		return nil, fmt.Errorf("read token from keychain: %w", err)
	}
	var t StoredToken
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return nil, fmt.Errorf("parse stored token: %w", err)
	}
	return &t, nil
}

func DeleteStoredToken(provider string) error {
	provider = normalizeProvider(provider)
	if provider == "" {
		return errors.New("provider is required")
	}
	if err := keyring.Delete(keyringService, keyringKey(provider)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete keychain token: %w", err)
	}
	_ = os.Remove(metaFilePath(provider))
	return nil
}

func readStoredTokenMetadata(provider string) (map[string]any, error) {
	provider = normalizeProvider(provider)
	b, err := os.ReadFile(metaFilePath(provider))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoStoredToken
		}
		return nil, fmt.Errorf("read token metadata: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse token metadata: %w", err)
	}
	return m, nil
}

func keyringKey(provider string) string {
	return "oauth:" + normalizeProvider(provider)
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func writeMetadataFile(provider string, meta map[string]any) error {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve user config dir: %w", err)
	}
	dir := filepath.Join(cfgDir, "runic")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create metadata dir: %w", err)
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaFilePath(provider), b, 0600); err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}
	return nil
}

func metaFilePath(provider string) string {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", fmt.Sprintf("runic-oauth-%s.json", provider))
	}
	return filepath.Join(cfgDir, "runic", fmt.Sprintf("oauth-%s.json", provider))
}
