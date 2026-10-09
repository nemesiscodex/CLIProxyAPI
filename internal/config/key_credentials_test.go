package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const keyCredentialsV8Config = `config-version: 8
access:
  api-keys:
    - main-key
    - limited-key
  key-credentials:
    - key: limited-key
      credentials:
        - claude-7cb6bfeb-*.json
        - codex-*.json
routing:
  retry:
    request-retry: 1
`

func TestKeyCredentialsLoadAndSaveRoundTrip(t *testing.T) {
	want := []ClientKeyCredentials{{Key: "limited-key", Credentials: []string{"claude-7cb6bfeb-*.json", "codex-*.json"}}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(keyCredentialsV8Config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateV8Config([]byte(keyCredentialsV8Config)); err != nil {
		t.Fatalf("access.key-credentials must be valid v8: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.KeyCredentials, want) {
		t.Fatalf("loaded key-credentials = %#v, want %#v", cfg.KeyCredentials, want)
	}

	cfg.RequestRetry = 2
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(saved, &doc); err != nil {
		t.Fatal(err)
	}
	if yamlPath(doc.Content[0], "access.key-credentials") == nil || yamlPath(doc.Content[0], "key-credentials") != nil {
		t.Fatalf("save must write key-credentials under access:\n%s", saved)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 2 || !reflect.DeepEqual(reloaded.KeyCredentials, want) {
		t.Fatalf("reloaded retry=%d key-credentials=%#v, want 2 and %#v", reloaded.RequestRetry, reloaded.KeyCredentials, want)
	}
}

func TestKeyCredentialsLegacyLayoutMigratesUnderAccess(t *testing.T) {
	legacy := "api-keys: [limited-key]\nkey-credentials:\n  - key: limited-key\n    credentials: [codex-*.json]\n"
	migrated, changed, err := NormalizeConfigLayout([]byte(legacy), true)
	if err != nil || !changed {
		t.Fatalf("NormalizeConfigLayout() changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(migrated), "access:") || strings.Contains(string(migrated), "\nkey-credentials:") {
		t.Fatalf("legacy key-credentials did not migrate under access:\n%s", migrated)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.KeyCredentials) != 1 || cfg.KeyCredentials[0].Key != "limited-key" {
		t.Fatalf("migrated key-credentials = %#v", cfg.KeyCredentials)
	}
}
