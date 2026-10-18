package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

type AccountGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Proxy     string `json:"proxy"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type accountGroupInput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Proxy   string `json:"proxy"`
	Enabled *bool  `json:"enabled"`
}

func (s *Store) LoadAccountGroups() ([]AccountGroup, error) {
	rows, err := s.db.Query("SELECT id, name, proxy, enabled, created_at, updated_at FROM app_account_groups ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []AccountGroup{}
	for rows.Next() {
		var group AccountGroup
		if err := rows.Scan(&group.ID, &group.Name, &group.Proxy, &group.Enabled, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) ValidateAccountGroupID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	var exists int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM app_account_groups WHERE id="+s.bind(1), id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errors.New("账号组不存在")
	}
	return nil
}

func (s *Store) SaveAccountGroup(input accountGroupInput) (AccountGroup, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return AccountGroup{}, errors.New("账号组名称不能为空")
	}
	proxy, err := s.normalizeAccountProxyAssignment(input.Proxy)
	if err != nil {
		return AccountGroup{}, err
	}
	if input.ID == "" {
		input.ID = "ag_" + randID(8)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := nowISO()
	createdAt := now
	tx, err := s.db.Begin()
	if err != nil {
		return AccountGroup{}, err
	}
	defer tx.Rollback()
	if err := lockCollectionRows(s, tx, "proxy_references"); err != nil {
		return AccountGroup{}, err
	}
	if strings.HasPrefix(proxy, "group:") {
		if err := s.ensureProxyGroupExistsTx(tx, strings.TrimPrefix(proxy, "group:")); err != nil {
			return AccountGroup{}, err
		}
	}
	err = tx.QueryRow("SELECT created_at FROM app_account_groups WHERE id="+s.bind(1), input.ID).Scan(&createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AccountGroup{}, err
	}
	query := "INSERT INTO app_account_groups (id, name, proxy, enabled, created_at, updated_at) VALUES (" + s.bind(1) + "," + s.bind(2) + "," + s.bind(3) + "," + s.bind(4) + "," + s.bind(5) + "," + s.bind(6) + ") ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, proxy=EXCLUDED.proxy, enabled=EXCLUDED.enabled, updated_at=EXCLUDED.updated_at"
	if _, err := tx.Exec(query, input.ID, input.Name, proxy, enabled, createdAt, now); err != nil {
		return AccountGroup{}, err
	}
	if err := tx.Commit(); err != nil {
		return AccountGroup{}, err
	}
	return AccountGroup{ID: input.ID, Name: input.Name, Proxy: proxy, Enabled: enabled, CreatedAt: createdAt, UpdatedAt: now}, nil
}

func (s *Store) DeleteAccountGroup(id string) error {
	id = strings.TrimSpace(id)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockCollectionRows(s, tx, accountRows.name); err != nil {
		return err
	}
	accounts, _, err := readCollectionRows[Account](tx, accountRows)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if account.GroupID == id {
			return errors.New("账号组仍有账号使用")
		}
	}
	result, err := tx.Exec("DELETE FROM app_account_groups WHERE id="+s.bind(1), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Server) handleAccountGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		groups, err := s.store.LoadAccountGroups()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	case http.MethodPost:
		var input accountGroupInput
		if !readBody(w, r, &input) {
			return
		}
		group, err := s.store.SaveAccountGroup(input)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"group": group})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleAccountGroupID(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/account-groups/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusBadRequest, "账号组 ID 无效")
		return
	}
	if err := s.store.DeleteAccountGroup(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "账号组不存在")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted_id": id})
}
