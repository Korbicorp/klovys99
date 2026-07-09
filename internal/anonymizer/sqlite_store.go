package anonymizer

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

const defaultSQLiteTokenStoreDSN = "file:anonymizer-token-store?mode=memory&cache=shared"

// SQLiteTokenStore stores token mappings in a shared SQLite database.
type SQLiteTokenStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSQLiteTokenStore opens or creates a shared SQLite token store.
func NewSQLiteTokenStore(path string) (*SQLiteTokenStore, error) {
	dsn := path
	if dsn == "" {
		dsn = defaultSQLiteTokenStoreDSN
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite token store: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &SQLiteTokenStore{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

// NewRunStore returns a per-run view backed by the shared SQLite mappings.
func (s *SQLiteTokenStore) NewRunStore() (RunTokenStore, error) {
	return &sqliteRunTokenStore{store: s}, nil
}

// Close releases the underlying SQLite connection.
func (s *SQLiteTokenStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}

	return s.db.Close()
}

func (s *SQLiteTokenStore) init() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS token_counters (
			entity_type TEXT PRIMARY KEY,
			next_id INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS token_mappings (
			entity_type TEXT NOT NULL,
			normalized_key TEXT NOT NULL,
			token TEXT NOT NULL UNIQUE,
			original_value TEXT NOT NULL,
			PRIMARY KEY (entity_type, normalized_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_mappings_token ON token_mappings(token)`,
	}

	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize sqlite token store: %w", err)
		}
	}

	return nil
}

func (s *SQLiteTokenStore) tokenFor(entityType EntityType, normalizedKey string, originalValue string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin sqlite token transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var token string
	err = tx.QueryRow(
		`SELECT token FROM token_mappings WHERE entity_type = ? AND normalized_key = ?`,
		string(entityType),
		normalizedKey,
	).Scan(&token)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("commit sqlite token lookup: %w", err)
		}
		return token, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("lookup sqlite token: %w", err)
	}

	nextID, err := nextSQLiteTokenID(tx, entityType)
	if err != nil {
		return "", err
	}

	token = fmt.Sprintf("[%s_%d]", entityType, nextID)
	if _, err := tx.Exec(
		`INSERT INTO token_mappings (entity_type, normalized_key, token, original_value) VALUES (?, ?, ?, ?)`,
		string(entityType),
		normalizedKey,
		token,
		originalValue,
	); err != nil {
		return "", fmt.Errorf("insert sqlite token mapping: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit sqlite token insert: %w", err)
	}

	return token, nil
}

func (s *SQLiteTokenStore) valueForToken(token string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var value string
	err := s.db.QueryRow(
		`SELECT original_value FROM token_mappings WHERE token = ?`,
		token,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup sqlite token value: %w", err)
	}

	return value, true, nil
}

func nextSQLiteTokenID(tx *sql.Tx, entityType EntityType) (int, error) {
	var nextID int
	err := tx.QueryRow(
		`SELECT next_id FROM token_counters WHERE entity_type = ?`,
		string(entityType),
	).Scan(&nextID)
	switch {
	case err == sql.ErrNoRows:
		nextID = 1
		if _, err := tx.Exec(
			`INSERT INTO token_counters (entity_type, next_id) VALUES (?, ?)`,
			string(entityType),
			nextID,
		); err != nil {
			return 0, fmt.Errorf("insert sqlite token counter: %w", err)
		}
		return nextID, nil
	case err != nil:
		return 0, fmt.Errorf("lookup sqlite token counter: %w", err)
	}

	nextID++
	if _, err := tx.Exec(
		`UPDATE token_counters SET next_id = ? WHERE entity_type = ?`,
		nextID,
		string(entityType),
	); err != nil {
		return 0, fmt.Errorf("update sqlite token counter: %w", err)
	}

	return nextID, nil
}

type sqliteRunTokenStore struct {
	store *SQLiteTokenStore
}

func (s *sqliteRunTokenStore) TokenFor(entityType EntityType, normalizedKey string, originalValue string) (string, error) {
	return s.store.tokenFor(entityType, normalizedKey, originalValue)
}

func (s *sqliteRunTokenStore) ValueForToken(token string) (string, bool, error) {
	return s.store.valueForToken(token)
}

func (s *sqliteRunTokenStore) Close() error {
	return nil
}
