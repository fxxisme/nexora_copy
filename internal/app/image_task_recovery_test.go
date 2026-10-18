package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverUnfinishedTasksRejectsUnreadableTaskData(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if _, err := store.db.Exec("INSERT INTO app_documents (name, payload) VALUES (?, ?)", "image_tasks.json", "not json"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	if err := s.recoverUnfinishedTasks(); err == nil || !strings.Contains(err.Error(), "读取未完成图片任务失败") {
		t.Fatalf("corrupt tasks recovery error = %v", err)
	}
}

func TestNewServerFailsWhenChargedTaskCannotRecover(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "data"))
	if err := store.SaveTasks([]ImageTask{{ID: "pending", OwnerID: "missing-key", Status: "running", ChargedCount: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(root)
	if err == nil || server != nil || !strings.Contains(err.Error(), "user key not found") {
		t.Fatalf("startup recovery: server=%v err=%v", server, err)
	}
}

func TestCleanupOldTasksReportsStorageFailure(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	s := &Server{store: store, cfg: Config{ImageRetentionDays: 1}}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupOldTasks(); err == nil || !strings.Contains(err.Error(), "清理过期图片任务失败") {
		t.Fatalf("cleanup error = %v", err)
	}
}
