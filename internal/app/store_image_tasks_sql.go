package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Image task quota mutations use one database transaction so a process crash
// cannot leave a terminal task and its user quota out of sync.
func (s *Store) loadImageTasksTx(tx *sql.Tx) ([]ImageTask, error) {
	query := "SELECT payload FROM app_documents WHERE name = " + s.bind(1)
	if s.backend == "postgres" {
		query += " FOR UPDATE"
	}
	var payload string
	if err := tx.QueryRow(query, "image_tasks.json").Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []ImageTask{}, nil
		}
		return nil, err
	}
	var tasks []ImageTask
	if err := json.Unmarshal([]byte(payload), &tasks); err != nil {
		return nil, fmt.Errorf("decode image_tasks.json: %w", err)
	}
	return tasks, nil
}

func (s *Store) saveImageTasksTx(tx *sql.Tx, tasks []ImageTask) error {
	data, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	stmt := "INSERT INTO app_documents (name, payload) VALUES (" + s.bind(1) + ", " + s.bind(2) + ") ON CONFLICT (name) DO UPDATE SET payload = EXCLUDED.payload"
	_, err = tx.Exec(stmt, "image_tasks.json", string(data))
	return err
}

func (s *Store) loadAuthKeyTx(tx *sql.Tx, id string) (UserKey, bool, error) {
	if err := lockCollectionRows(s, tx, "auth_keys"); err != nil {
		return UserKey{}, false, err
	}
	query := "SELECT payload FROM app_auth_keys WHERE key_id = " + s.bind(1)
	if s.backend == "postgres" {
		query += " FOR UPDATE"
	}
	var payload string
	if err := tx.QueryRow(query, id).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UserKey{}, false, nil
		}
		return UserKey{}, false, err
	}
	var key UserKey
	if err := json.Unmarshal([]byte(payload), &key); err != nil {
		return UserKey{}, false, fmt.Errorf("decode auth key %q: %w", id, err)
	}
	return normalizeKeys([]UserKey{key})[0], true, nil
}

func (s *Store) saveAuthKeyTx(tx *sql.Tx, key UserKey) error {
	data, err := json.Marshal(key)
	if err != nil {
		return err
	}
	result, err := tx.Exec("UPDATE app_auth_keys SET payload = "+s.bind(1)+" WHERE key_id = "+s.bind(2), string(data), key.ID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("user key %q was not updated", key.ID)
	}
	return nil
}

func updateImageTaskQuota(key *UserKey, n int, refund bool) {
	if n <= 0 || key == nil {
		return
	}
	if refund {
		key.ImageDailyUsed = maxInt(0, key.ImageDailyUsed-n)
		key.ImageMonthlyUsed = maxInt(0, key.ImageMonthlyUsed-n)
		key.ImageTotalUsed = maxInt(0, key.ImageTotalUsed-n)
		return
	}
	key.ImageDailyUsed += n
	key.ImageMonthlyUsed += n
	key.ImageTotalUsed += n
}

func imageTaskQuotaAvailable(key UserKey, n int) bool {
	resetPeriods(&key)
	return enough(key.ImageDailyQuota, key.ImageDailyUsed, key.ImageDailyUnlimited, n) &&
		enough(key.ImageMonthlyQuota, key.ImageMonthlyUsed, key.ImageMonthlyUnlimited, n) &&
		enough(key.ImageTotalQuota, key.ImageTotalUsed, key.ImageTotalUnlimited, n)
}

func (s *Store) chargeImageTask(ownerID, taskID string, n int, unlimited bool) (bool, error) {
	if n < 1 {
		n = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	tasks, err := s.loadImageTasksTx(tx)
	if err != nil {
		return false, err
	}
	index := -1
	for i := range tasks {
		if tasks[i].OwnerID == ownerID && tasks[i].ID == taskID && isActiveImageTaskStatus(tasks[i].Status) {
			index = i
			break
		}
	}
	if index < 0 {
		return false, nil
	}
	var key UserKey
	if !unlimited {
		var ok bool
		key, ok, err = s.loadAuthKeyTx(tx, ownerID)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.New("user key not found")
		}
		if !imageTaskQuotaAvailable(key, n) {
			return false, nil
		}
		updateImageTaskQuota(&key, n, false)
	}
	tasks[index].ChargedCount = n
	tasks[index].QuotaSettled = false
	if err := s.saveImageTasksTx(tx, tasks); err != nil {
		return false, err
	}
	if !unlimited {
		if err := s.saveAuthKeyTx(tx, key); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) settleImageTaskWithQuota(ownerID, taskID, status, errText string, data []map[string]any, refund int) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	tasks, err := s.loadImageTasksTx(tx)
	if err != nil {
		return false, err
	}
	index := -1
	for i := range tasks {
		if tasks[i].OwnerID == ownerID && tasks[i].ID == taskID && isActiveImageTaskStatus(tasks[i].Status) {
			index = i
			break
		}
	}
	if index < 0 {
		return false, nil
	}
	task := &tasks[index]
	if refund < 0 {
		refund = 0
	}
	if task.ChargedCount > 0 && refund > task.ChargedCount {
		refund = task.ChargedCount
	}
	if refund > 0 {
		key, ok, err := s.loadAuthKeyTx(tx, ownerID)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.New("user key not found")
		}
		updateImageTaskQuota(&key, refund, true)
		if err := s.saveAuthKeyTx(tx, key); err != nil {
			return false, err
		}
	}
	task.Status = status
	task.Error = errText
	task.Data = data
	task.Terminal = !isActiveImageTaskStatus(status)
	task.StageCode, task.StageLabel = imageTaskStage(status)
	if status == "running" && task.StartedAt == "" {
		task.StartedAt = nowISO()
	}
	requested := max(1, task.RequestedCount)
	if task.Mode == "edit" {
		requested = 1
	}
	task.RequestedCount = requested
	task.SucceededCount = min(requested, len(data))
	if task.Terminal {
		task.PendingCount = 0
		task.FailedCount = max(0, requested-task.SucceededCount)
		task.DurationMS = time.Since(parseTaskCreatedAt(task.CreatedAt)).Milliseconds()
		task.ElapsedMS = task.DurationMS
	} else {
		task.PendingCount = max(0, requested-task.SucceededCount)
		task.FailedCount = 0
	}
	task.ChargedCount = 0
	task.QuotaSettled = true
	task.UpdatedAt = nowISO()
	if err := s.saveImageTasksTx(tx, tasks); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) recoverImageTask(ownerID, taskID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	tasks, err := s.loadImageTasksTx(tx)
	if err != nil {
		return false, err
	}
	index := -1
	for i := range tasks {
		if tasks[i].OwnerID == ownerID && tasks[i].ID == taskID && isActiveImageTaskStatus(tasks[i].Status) {
			index = i
			break
		}
	}
	if index < 0 {
		return false, nil
	}
	task := &tasks[index]
	refund := task.ChargedCount
	if refund > 0 {
		key, ok, err := s.loadAuthKeyTx(tx, ownerID)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.New("user key not found")
		}
		updateImageTaskQuota(&key, refund, true)
		if err := s.saveAuthKeyTx(tx, key); err != nil {
			return false, err
		}
	}
	task.Status = "error"
	task.Error = "server restarted"
	task.Terminal = true
	task.StageCode, task.StageLabel = imageTaskStage("error")
	requested := max(1, task.RequestedCount)
	if task.Mode == "edit" {
		requested = 1
	}
	task.RequestedCount = requested
	task.SucceededCount = min(requested, len(task.Data))
	task.PendingCount = 0
	task.FailedCount = max(0, requested-task.SucceededCount)
	task.DurationMS = time.Since(parseTaskCreatedAt(task.CreatedAt)).Milliseconds()
	task.ElapsedMS = task.DurationMS
	task.ChargedCount = 0
	task.QuotaSettled = true
	task.UpdatedAt = nowISO()
	if err := s.saveImageTasksTx(tx, tasks); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
