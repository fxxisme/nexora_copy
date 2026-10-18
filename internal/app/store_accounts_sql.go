package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
)

type collectionRows struct {
	table, keyColumn, name string
}

var accountRows = collectionRows{"app_accounts", "token_hash", "accounts"}
var authKeyRows = collectionRows{"app_auth_keys", "key_id", "auth_keys"}

type rowQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

type storedRow struct {
	ordinal int64
	payload string
}

func readCollectionRows[T any](queryer rowQueryer, spec collectionRows) ([]T, map[string]storedRow, error) {
	query := "SELECT " + spec.keyColumn + ", ordinal, payload FROM " + spec.table + " ORDER BY ordinal, " + spec.keyColumn
	rows, err := queryer.Query(query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []T{}
	stored := map[string]storedRow{}
	for rows.Next() {
		var key, payload string
		var ordinal int64
		if err := rows.Scan(&key, &ordinal, &payload); err != nil {
			return nil, nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, nil, fmt.Errorf("decode %s row %q: %w", spec.name, key, err)
		}
		items = append(items, item)
		stored[key] = storedRow{ordinal: ordinal, payload: payload}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, stored, nil
}

func loadAccountRows(s *Store) ([]Account, error) {
	items, _, err := readCollectionRows[Account](s.db, accountRows)
	return items, err
}

func loadAuthKeyRows(s *Store) ([]UserKey, error) {
	items, _, err := readCollectionRows[UserKey](s.db, authKeyRows)
	return items, err
}

func lockCollectionRows(s *Store, tx *sql.Tx, name string) error {
	lock := "SELECT version FROM app_collection_locks WHERE name = " + s.bind(1)
	if s.backend == "postgres" {
		lock += " FOR UPDATE"
	}
	var version int64
	if err := tx.QueryRow(lock, name).Scan(&version); err != nil {
		return fmt.Errorf("lock %s collection: %w", name, err)
	}
	return nil
}

func replaceCollectionRows[T any](s *Store, spec collectionRows, mutate func([]T) []T, keyFor func(T) (string, error), validate func([]T, []T, *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockCollectionRows(s, tx, spec.name); err != nil {
		return err
	}
	items, stored, err := readCollectionRows[T](tx, spec)
	if err != nil {
		return err
	}
	desired := mutate(items)
	if validate != nil {
		if err := validate(items, desired, tx); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(desired))
	changed := false
	for i, item := range desired {
		key, err := keyFor(item)
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("duplicate %s key %q", spec.name, key)
		}
		seen[key] = true
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		payload := string(encoded)
		previous, exists := stored[key]
		if exists && previous.ordinal == int64(i) && previous.payload == payload {
			continue
		}
		query := "INSERT INTO " + spec.table + " (" + spec.keyColumn + ", ordinal, payload) VALUES (" + s.bind(1) + ", " + s.bind(2) + ", " + s.bind(3) + ") ON CONFLICT (" + spec.keyColumn + ") DO UPDATE SET ordinal = EXCLUDED.ordinal, payload = EXCLUDED.payload"
		if _, err := tx.Exec(query, key, i, payload); err != nil {
			return err
		}
		changed = true
	}
	for key := range stored {
		if seen[key] {
			continue
		}
		if _, err := tx.Exec("DELETE FROM "+spec.table+" WHERE "+spec.keyColumn+" = "+s.bind(1), key); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if _, err := tx.Exec("UPDATE app_collection_locks SET version = version + 1 WHERE name = "+s.bind(1), spec.name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func replaceAccountRows(s *Store, mutate func([]Account) []Account) error {
	return replaceCollectionRows(s, accountRows, func(items []Account) []Account {
		return mutate(append([]Account(nil), items...))
	}, func(account Account) (string, error) {
		if account.AccessToken == "" {
			return "", errors.New("account access_token is required")
		}
		return hashKey(account.AccessToken), nil
	}, func(original, desired []Account, tx *sql.Tx) error {
		oldGroups := make(map[string]string, len(original))
		oldProxies := make(map[string]string, len(original))
		for _, account := range original {
			oldGroups[hashKey(account.AccessToken)] = account.GroupID
			oldProxies[hashKey(account.AccessToken)] = account.Proxy
		}
		checked := map[string]bool{}
		proxyGroups := map[string]bool{}
		for _, account := range desired {
			key := hashKey(account.AccessToken)
			id := strings.TrimSpace(account.GroupID)
			if id != "" && id != oldGroups[key] && !checked[id] {
				var exists int
				if err := tx.QueryRow("SELECT COUNT(*) FROM app_account_groups WHERE id="+s.bind(1), id).Scan(&exists); err != nil {
					return err
				}
				if exists == 0 {
					return errors.New("账号组不存在")
				}
				checked[id] = true
			}
			if account.Proxy != oldProxies[key] && strings.HasPrefix(account.Proxy, "group:") {
				proxyGroups[strings.TrimPrefix(account.Proxy, "group:")] = true
			}
		}
		if len(proxyGroups) > 0 {
			if err := lockCollectionRows(s, tx, "proxy_references"); err != nil {
				return err
			}
			for id := range proxyGroups {
				if err := s.ensureProxyGroupExistsTx(tx, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func replaceAuthKeyRows(s *Store, mutate func([]UserKey) []UserKey) error {
	return replaceCollectionRows(s, authKeyRows, func(items []UserKey) []UserKey {
		return normalizeKeys(mutate(items))
	}, func(key UserKey) (string, error) {
		if key.ID == "" {
			return "", errors.New("user key ID is required")
		}
		return key.ID, nil
	}, nil)
}

func (s *Store) migrateAccountRows() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.backend == "postgres" {
		if _, err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('chatgpt2api_account_rows_v1'))"); err != nil {
			return err
		}
	}
	var marker string
	err = tx.QueryRow("SELECT name FROM app_migrations WHERE name = 'account_rows_v1'").Scan(&marker)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	accounts, err := legacyAccountDocument(tx, s)
	if err != nil {
		return err
	}
	keys, err := legacyAuthKeyDocument(tx, s)
	if err != nil {
		return err
	}
	var accountCount, keyCount int
	if err := tx.QueryRow("SELECT COUNT(*) FROM app_accounts").Scan(&accountCount); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM app_auth_keys").Scan(&keyCount); err != nil {
		return err
	}
	if accountCount == 0 {
		for i, account := range accounts {
			if account.AccessToken == "" {
				continue
			}
			encoded, err := json.Marshal(account)
			if err != nil {
				return err
			}
			query := "INSERT INTO app_accounts (token_hash, ordinal, payload) VALUES (" + s.bind(1) + ", " + s.bind(2) + ", " + s.bind(3) + ")"
			if _, err := tx.Exec(query, hashKey(account.AccessToken), i, string(encoded)); err != nil {
				return fmt.Errorf("migrate account %d: %w", i, err)
			}
			accountCount++
		}
	}
	if keyCount == 0 {
		for i, key := range normalizeKeys(keys) {
			encoded, err := json.Marshal(key)
			if err != nil {
				return err
			}
			query := "INSERT INTO app_auth_keys (key_id, ordinal, payload) VALUES (" + s.bind(1) + ", " + s.bind(2) + ", " + s.bind(3) + ")"
			if _, err := tx.Exec(query, key.ID, i, string(encoded)); err != nil {
				return fmt.Errorf("migrate auth key %d: %w", i, err)
			}
			keyCount++
		}
	}
	if _, err := tx.Exec("INSERT INTO app_migrations (name) VALUES ('account_rows_v1') ON CONFLICT (name) DO NOTHING"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if accountCount > 0 || keyCount > 0 {
		log.Printf("storage migrated account rows: accounts=%d auth_keys=%d", accountCount, keyCount)
	}
	return nil
}

func legacyAccountDocument(tx *sql.Tx, s *Store) ([]Account, error) {
	var payload string
	err := tx.QueryRow("SELECT payload FROM app_documents WHERE name = "+s.bind(1), "accounts.json").Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var accounts []Account
	if err := json.Unmarshal([]byte(payload), &accounts); err != nil {
		return nil, fmt.Errorf("decode existing accounts document: %w", err)
	}
	return accounts, nil
}

func legacyAuthKeyDocument(tx *sql.Tx, s *Store) ([]UserKey, error) {
	var payload string
	err := tx.QueryRow("SELECT payload FROM app_documents WHERE name = "+s.bind(1), "auth_keys.json").Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []UserKey
	if strings.HasPrefix(strings.TrimSpace(payload), "[") {
		if err := json.Unmarshal([]byte(payload), &keys); err != nil {
			return nil, fmt.Errorf("decode existing auth keys document: %w", err)
		}
	} else {
		var wrapped authKeysWrap
		if err := json.Unmarshal([]byte(payload), &wrapped); err != nil {
			return nil, fmt.Errorf("decode existing auth keys document: %w", err)
		}
		keys = wrapped.Items
	}
	return keys, nil
}
