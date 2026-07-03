package appconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// TestNewStoreCreatesDefaultConfig verifies that a missing config file is created with all types enabled.
func TestNewStoreCreatesDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "klovys99_config.json")

	store, err := NewStore(path, []anonymizer.EntityType{anonymizer.EntityEmail, anonymizer.EntitySecret})
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	if !store.IsTypeEnabled(anonymizer.EntityEmail) || !store.IsTypeEnabled(anonymizer.EntitySecret) {
		t.Fatal("default config should enable every known type")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	var config Config
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("parse config file: %v", err)
	}
	if !config.ProtectionOptions.Types[anonymizer.EntityEmail] || !config.ProtectionOptions.Types[anonymizer.EntitySecret] {
		t.Fatalf("config = %#v, want all known types enabled", config)
	}
}

// TestUpdateProtectionOptionsPersistsConfig verifies that saved toggles survive a new store instance.
func TestUpdateProtectionOptionsPersistsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "klovys99_config.json")
	knownTypes := []anonymizer.EntityType{anonymizer.EntityEmail, anonymizer.EntitySecret}
	store, err := NewStore(path, knownTypes)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	options, err := store.UpdateProtectionOptions([]ProtectionOption{
		{Type: anonymizer.EntityEmail, Enabled: false},
		{Type: anonymizer.EntitySecret, Enabled: true},
	})
	if err != nil {
		t.Fatalf("UpdateProtectionOptions returned error: %v", err)
	}
	if len(options) != 2 || options[0].Type != anonymizer.EntityEmail || options[0].Enabled {
		t.Fatalf("options = %#v, want EMAIL disabled first", options)
	}

	reloaded, err := NewStore(path, knownTypes)
	if err != nil {
		t.Fatalf("reloaded NewStore returned error: %v", err)
	}
	if reloaded.IsTypeEnabled(anonymizer.EntityEmail) {
		t.Fatal("EMAIL should remain disabled after reload")
	}
	if !reloaded.IsTypeEnabled(anonymizer.EntitySecret) {
		t.Fatal("SECRET should remain enabled after reload")
	}
}

// TestUpdateProtectionOptionsLogsConfigChange verifies that persisted config edits are visible in info logs.
func TestUpdateProtectionOptionsLogsConfigChange(t *testing.T) {
	previousLogger := log.Logger
	var logs bytes.Buffer
	log.Logger = zerolog.New(&logs)
	t.Cleanup(func() {
		log.Logger = previousLogger
	})

	path := filepath.Join(t.TempDir(), "klovys99_config.json")
	store, err := NewStore(path, []anonymizer.EntityType{anonymizer.EntityEmail, anonymizer.EntitySecret})
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	logs.Reset()

	if _, err := store.UpdateProtectionOptions([]ProtectionOption{
		{Type: anonymizer.EntityEmail, Enabled: false},
		{Type: anonymizer.EntitySecret, Enabled: true},
	}); err != nil {
		t.Fatalf("UpdateProtectionOptions returned error: %v", err)
	}

	got := logs.String()
	for _, want := range []string{
		`"message":"app config updated"`,
		`"changed_types":["EMAIL"]`,
		`"enabled_types":1`,
		`"disabled_types":1`,
		`"total_types":2`,
		path,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("logs = %q, want containing %q", got, want)
		}
	}
}

// TestUpdateProtectionOptionsAllowsAllDisabled verifies that users can intentionally turn every type off.
func TestUpdateProtectionOptionsAllowsAllDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "klovys99_config.json")
	store, err := NewStore(path, []anonymizer.EntityType{anonymizer.EntityEmail, anonymizer.EntitySecret})
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	options, err := store.UpdateProtectionOptions([]ProtectionOption{
		{Type: anonymizer.EntityEmail, Enabled: false},
		{Type: anonymizer.EntitySecret, Enabled: false},
	})
	if err != nil {
		t.Fatalf("UpdateProtectionOptions returned error: %v", err)
	}

	for _, option := range options {
		if option.Enabled {
			t.Fatalf("options = %#v, want every type disabled", options)
		}
	}
	if store.IsTypeEnabled(anonymizer.EntityEmail) || store.IsTypeEnabled(anonymizer.EntitySecret) {
		t.Fatal("all configured types should be disabled")
	}
}

// TestUpdateProtectionOptionsRejectsUnknownTypes verifies that API updates cannot persist unsupported toggles.
func TestUpdateProtectionOptionsRejectsUnknownTypes(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "klovys99_config.json"), []anonymizer.EntityType{anonymizer.EntityEmail})
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	if _, err := store.UpdateProtectionOptions([]ProtectionOption{{Type: "UNKNOWN", Enabled: true}}); err == nil {
		t.Fatal("UpdateProtectionOptions returned nil error, want unknown type error")
	}
}

// TestMissingTypesDefaultToEnabled verifies that hand-written partial configs do not disable new types.
func TestMissingTypesDefaultToEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "klovys99_config.json")
	content := []byte(`{"protection_options":{"types":{"EMAIL":false}}}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, []anonymizer.EntityType{anonymizer.EntityEmail, anonymizer.EntitySecret})
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if store.IsTypeEnabled(anonymizer.EntityEmail) {
		t.Fatal("EMAIL should remain disabled from hand-written config")
	}
	if !store.IsTypeEnabled(anonymizer.EntitySecret) {
		t.Fatal("missing SECRET should default to enabled")
	}
}
