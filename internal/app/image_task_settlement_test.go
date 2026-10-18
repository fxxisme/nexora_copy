package app

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRetryImageTaskSettlementRecoversTransientFailure(t *testing.T) {
	attempts := 0
	updated, err := retryImageTaskSettlement(func() (bool, error) {
		attempts++
		if attempts < 3 {
			return false, errors.New("database is busy")
		}
		return true, nil
	})
	if !updated || err != nil || attempts != 3 {
		t.Fatalf("settlement retry: updated=%t err=%v attempts=%d", updated, err, attempts)
	}
}

func TestRetryImageTaskSettlementStopsAfterBoundedAttempts(t *testing.T) {
	attempts := 0
	updated, err := retryImageTaskSettlement(func() (bool, error) {
		attempts++
		return false, errors.New("database unavailable")
	})
	if updated || err == nil || attempts != 3 {
		t.Fatalf("settlement retry: updated=%t err=%v attempts=%d", updated, err, attempts)
	}
}

func TestImageTaskSettlementConfirmedOnlyForPersistedResult(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	task := ImageTask{ID: "task", OwnerID: "owner", Status: "running", RequestedCount: 2, ChargedCount: 2}
	if err := store.SaveTasks([]ImageTask{task}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	if s.imageTaskSettlementConfirmed(task, "partial_success", 1) {
		t.Fatal("active task was confirmed as settled")
	}
	if err := store.UpdateTasks(func(tasks []ImageTask) []ImageTask {
		tasks[0].Status = "partial_success"
		tasks[0].QuotaSettled = true
		tasks[0].Data = []map[string]any{{"url": "saved"}}
		return tasks
	}); err != nil {
		t.Fatal(err)
	}
	if !s.imageTaskSettlementConfirmed(task, "partial_success", 1) || s.imageTaskSettlementConfirmed(task, "success", 1) || s.imageTaskSettlementConfirmed(task, "partial_success", 2) {
		t.Fatal("persisted result confirmation did not match status and delivered count")
	}
}
