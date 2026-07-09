package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/rs/zerolog/log"
)

const (
	// DefaultPath is the global app config file used by backend-only and dashboard users.
	DefaultPath = "klovys99_config.json"
)

// Config is the persisted global application configuration.
type Config struct {
	ProtectionOptions ProtectionOptions `json:"protection_options"`
}

// ProtectionOptions stores anonymization type toggles as a hand-editable bool map.
type ProtectionOptions struct {
	Types map[anonymizer.EntityType]bool `json:"types"`
}

// ProtectionOption is the dashboard/API representation of one type toggle.
type ProtectionOption struct {
	Type    anonymizer.EntityType `json:"type"`
	Enabled bool                  `json:"enabled"`
}

// Store owns the global config file and serializes reads and writes.
type Store struct {
	path       string
	knownTypes []anonymizer.EntityType
	mu         sync.RWMutex
	config     Config
}

func canonicalProtectionType(entityType anonymizer.EntityType) anonymizer.EntityType {
	if entityType == anonymizer.EntityPersonName {
		return anonymizer.EntityName
	}
	return entityType
}

// NewStore loads the global config file or creates it with all known types enabled.
func NewStore(path string, knownTypes []anonymizer.EntityType) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultPath
	}
	if len(knownTypes) == 0 {
		knownTypes = anonymizer.KnownEntityTypes()
	}
	store := &Store{
		path:       path,
		knownTypes: sortedUniqueEntityTypes(knownTypes),
	}
	if err := store.loadOrCreate(); err != nil {
		return nil, err
	}
	return store, nil
}

// Snapshot returns a normalized copy of the current persisted config.
func (s *Store) Snapshot() Config {
	if s == nil {
		return defaultConfig(anonymizer.KnownEntityTypes())
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeConfig(copyConfig(s.config), s.knownTypes)
}

// ProtectionOptions returns all known type toggles sorted for stable API responses.
func (s *Store) ProtectionOptions() []ProtectionOption {
	if s == nil {
		return configOptions(defaultConfig(anonymizer.KnownEntityTypes()), anonymizer.KnownEntityTypes())
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return configOptions(s.config, s.knownTypes)
}

// UpdateProtectionOptions replaces the type toggle map and persists the updated config.
func (s *Store) UpdateProtectionOptions(options []ProtectionOption) ([]ProtectionOption, error) {
	if s == nil {
		return nil, nil
	}
	types, err := optionsToTypeMap(options, s.knownTypes)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	previous := normalizeConfig(copyConfig(s.config), s.knownTypes)
	next := copyConfig(s.config)
	next.ProtectionOptions.Types = types
	next = normalizeConfig(next, s.knownTypes)
	if err := s.writeLocked(next); err != nil {
		return nil, err
	}
	s.config = next
	logConfigUpdate(s.path, previous, next, s.knownTypes)
	return configOptions(s.config, s.knownTypes), nil
}

// IsTypeEnabled reports whether a type should be anonymized under the current config.
func (s *Store) IsTypeEnabled(entityType anonymizer.EntityType) bool {
	if s == nil || entityType == "" {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	enabled, ok := s.config.ProtectionOptions.Types[entityType]
	if !ok {
		return true
	}
	return enabled
}

// Path returns the config file path owned by this store.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// loadOrCreate reads the config from disk or creates the default config file.
func (s *Store) loadOrCreate() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	content, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read app config: %w", err)
		}
		config := defaultConfig(s.knownTypes)
		if err := s.writeLocked(config); err != nil {
			return err
		}
		s.config = config
		logConfigCreated(s.path, config, s.knownTypes)
		return nil
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		s.config = defaultConfig(s.knownTypes)
		return nil
	}

	var config Config
	if err := json.Unmarshal(content, &config); err != nil {
		return fmt.Errorf("parse app config: %w", err)
	}
	s.config = normalizeConfig(config, s.knownTypes)
	return nil
}

// writeLocked writes a config file with stable formatting.
func (s *Store) writeLocked(config Config) error {
	if err := ensureParentDir(s.path); err != nil {
		return err
	}
	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal app config: %w", err)
	}
	content = append(content, '\n')
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
		return fmt.Errorf("write app config: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace app config: %w", err)
	}
	return nil
}

// logConfigCreated records that the app created a default config file.
func logConfigCreated(path string, config Config, knownTypes []anonymizer.EntityType) {
	enabled, disabled := protectionOptionStats(config, knownTypes)
	log.Info().
		Str("path", path).
		Int("enabled_types", enabled).
		Int("disabled_types", disabled).
		Int("total_types", enabled+disabled).
		Msg("app config created")
}

// logConfigUpdate records the persisted result of a dashboard or API config change.
func logConfigUpdate(path string, previous, next Config, knownTypes []anonymizer.EntityType) {
	enabled, disabled := protectionOptionStats(next, knownTypes)
	log.Info().
		Str("path", path).
		Strs("changed_types", changedProtectionTypes(previous, next, knownTypes)).
		Int("enabled_types", enabled).
		Int("disabled_types", disabled).
		Int("total_types", enabled+disabled).
		Msg("app config updated")
}

// protectionOptionStats counts enabled and disabled known types in one config.
func protectionOptionStats(config Config, knownTypes []anonymizer.EntityType) (int, int) {
	config = normalizeConfig(copyConfig(config), knownTypes)
	var enabled int
	for _, entityType := range knownTypes {
		if config.ProtectionOptions.Types[entityType] {
			enabled++
		}
	}
	return enabled, len(knownTypes) - enabled
}

// changedProtectionTypes returns known types whose enabled state changed.
func changedProtectionTypes(previous, next Config, knownTypes []anonymizer.EntityType) []string {
	previous = normalizeConfig(copyConfig(previous), knownTypes)
	next = normalizeConfig(copyConfig(next), knownTypes)
	changed := make([]string, 0)
	for _, entityType := range knownTypes {
		if previous.ProtectionOptions.Types[entityType] != next.ProtectionOptions.Types[entityType] {
			changed = append(changed, string(entityType))
		}
	}
	return changed
}

// ensureParentDir creates the directory that will contain the config file.
func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create app config directory: %w", err)
	}
	return nil
}

// defaultConfig enables every known anonymization type.
func defaultConfig(knownTypes []anonymizer.EntityType) Config {
	types := make(map[anonymizer.EntityType]bool, len(knownTypes))
	for _, entityType := range knownTypes {
		if entityType == "" {
			continue
		}
		types[entityType] = true
	}
	return Config{ProtectionOptions: ProtectionOptions{Types: types}}
}

// normalizeConfig fills missing sections and defaults missing known types to enabled.
func normalizeConfig(config Config, knownTypes []anonymizer.EntityType) Config {
	if config.ProtectionOptions.Types == nil {
		config.ProtectionOptions.Types = make(map[anonymizer.EntityType]bool, len(knownTypes))
	}
	mergeLegacyPersonNameType(config.ProtectionOptions.Types)
	for _, entityType := range knownTypes {
		if entityType == "" {
			continue
		}
		if _, ok := config.ProtectionOptions.Types[entityType]; !ok {
			config.ProtectionOptions.Types[entityType] = true
		}
	}
	return config
}

func mergeLegacyPersonNameType(types map[anonymizer.EntityType]bool) {
	enabled, hasName := types[anonymizer.EntityName]
	legacyEnabled, hasLegacy := types[anonymizer.EntityPersonName]
	switch {
	case hasName && hasLegacy:
		types[anonymizer.EntityName] = enabled && legacyEnabled
	case !hasName && hasLegacy:
		types[anonymizer.EntityName] = legacyEnabled
	}
	delete(types, anonymizer.EntityPersonName)
}

// copyConfig makes a deep copy so callers cannot mutate store state.
func copyConfig(config Config) Config {
	copied := Config{
		ProtectionOptions: ProtectionOptions{
			Types: make(map[anonymizer.EntityType]bool, len(config.ProtectionOptions.Types)),
		},
	}
	for entityType, enabled := range config.ProtectionOptions.Types {
		copied.ProtectionOptions.Types[entityType] = enabled
	}
	return copied
}

// configOptions converts the bool map into sorted dashboard/API options.
func configOptions(config Config, knownTypes []anonymizer.EntityType) []ProtectionOption {
	config = normalizeConfig(copyConfig(config), knownTypes)
	options := make([]ProtectionOption, 0, len(knownTypes))
	for _, entityType := range knownTypes {
		options = append(options, ProtectionOption{
			Type:    entityType,
			Enabled: config.ProtectionOptions.Types[entityType],
		})
	}
	return options
}

// optionsToTypeMap validates API options and converts them into the persisted bool map.
func optionsToTypeMap(options []ProtectionOption, knownTypes []anonymizer.EntityType) (map[anonymizer.EntityType]bool, error) {
	known := make(map[anonymizer.EntityType]struct{}, len(knownTypes))
	for _, entityType := range knownTypes {
		known[entityType] = struct{}{}
	}
	result := make(map[anonymizer.EntityType]bool, len(knownTypes))
	seen := make(map[anonymizer.EntityType]anonymizer.EntityType, len(options))
	for _, option := range options {
		entityType := anonymizer.EntityType(strings.TrimSpace(string(option.Type)))
		if entityType == "" {
			return nil, fmt.Errorf("protection option type is required")
		}
		canonicalType := canonicalProtectionType(entityType)
		if _, ok := known[canonicalType]; !ok {
			return nil, fmt.Errorf("unknown protection option type %q", entityType)
		}
		if previous, ok := seen[canonicalType]; ok {
			if entityType == anonymizer.EntityPersonName || previous == anonymizer.EntityPersonName {
				result[canonicalType] = result[canonicalType] && option.Enabled
				continue
			}
			return nil, fmt.Errorf("duplicate protection option type %q", entityType)
		}
		seen[canonicalType] = entityType
		result[canonicalType] = option.Enabled
	}
	for _, entityType := range knownTypes {
		if _, ok := result[entityType]; !ok {
			result[entityType] = true
		}
	}
	return result, nil
}

// sortedUniqueEntityTypes returns entity types in stable lexical order.
func sortedUniqueEntityTypes(entityTypes []anonymizer.EntityType) []anonymizer.EntityType {
	seen := make(map[anonymizer.EntityType]struct{}, len(entityTypes))
	result := make([]anonymizer.EntityType, 0, len(entityTypes))
	for _, entityType := range entityTypes {
		if entityType == "" {
			continue
		}
		if _, ok := seen[entityType]; ok {
			continue
		}
		seen[entityType] = struct{}{}
		result = append(result, entityType)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result
}
