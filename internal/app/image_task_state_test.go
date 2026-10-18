package app

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCancelImageTaskPreservesOtherUpdatesAndTerminalState(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}, taskCancels: map[imageTaskKey]context.CancelFunc{}}
	if err := store.SaveTasks([]ImageTask{
		{ID: "cancel-me", OwnerID: "admin", Status: "running"},
		{ID: "keep-me", OwnerID: "admin", Status: "running"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.setTaskCancel("admin", "cancel-me", cancel)
	request := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(`{"ids":["cancel-me","missing"]}`))
	request.Header.Set("Authorization", "Bearer admin-key")
	response := httptest.NewRecorder()
	s.handleImageTaskCancel(response, request)
	if response.Code != http.StatusOK || ctx.Err() == nil {
		t.Fatalf("cancel response = %d %s, context = %v", response.Code, response.Body.String(), ctx.Err())
	}
	var payload struct {
		Canceled []string `json:"canceled"`
		Missing  []string `json:"missing_ids"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Canceled) != 1 || payload.Canceled[0] != "cancel-me" || len(payload.Missing) != 1 || payload.Missing[0] != "missing" {
		t.Fatalf("cancel result = %+v", payload)
	}
	if updated, err := s.updateTaskStatus("admin", "cancel-me", "success", "", []map[string]any{{"url": "late-result"}}); err != nil || updated {
		t.Fatalf("canceled task accepted late completion: updated=%t err=%v", updated, err)
	}
	if updated, err := s.updateTaskStatus("admin", "keep-me", "success", "", []map[string]any{{"url": "kept-result"}}); err != nil || !updated {
		t.Fatalf("active task completion failed: updated=%t err=%v", updated, err)
	}
	tasks := store.LoadTasks()
	if tasks[0].Status != "canceled" || tasks[0].Error != "canceled" || len(tasks[0].Data) != 0 {
		t.Fatalf("canceled task was resurrected: %+v", tasks[0])
	}
	if tasks[1].Status != "success" || len(tasks[1].Data) != 1 {
		t.Fatalf("unrelated update was lost: %+v", tasks[1])
	}
}

func TestImageTaskCancelAndCompletionRaceHasOneTerminalResult(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}, taskCancels: map[imageTaskKey]context.CancelFunc{}}
	for attempt := range 20 {
		id := "task-" + string(rune('a'+attempt))
		if err := store.UpdateTasks(func(tasks []ImageTask) []ImageTask {
			return append(tasks, ImageTask{ID: id, OwnerID: "admin", Status: "running"})
		}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			s.updateTaskStatus("admin", id, "success", "", nil)
		}()
		go func() {
			defer workers.Done()
			<-start
			request := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(`{"id":"`+id+`"}`))
			request.Header.Set("Authorization", "Bearer admin-key")
			s.handleImageTaskCancel(httptest.NewRecorder(), request)
		}()
		close(start)
		workers.Wait()
		found := false
		for _, task := range store.LoadTasks() {
			if task.ID != id {
				continue
			}
			found = true
			if task.Status != "success" && task.Status != "canceled" {
				t.Fatalf("task %s has nonterminal state %q", id, task.Status)
			}
		}
		if !found {
			t.Fatalf("task %s disappeared during cancel/complete race", id)
		}
	}
}

func TestRecoverUnfinishedImageTasksIncludesPending(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store}
	if err := store.SaveTasks([]ImageTask{
		{ID: "pending", Status: "pending"},
		{ID: "queued", Status: "queued"},
		{ID: "done", Status: "success"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverUnfinishedTasks(); err != nil {
		t.Fatal(err)
	}
	tasks := store.LoadTasks()
	if tasks[0].Status != "error" || tasks[1].Status != "error" || tasks[2].Status != "success" {
		t.Fatalf("recovery statuses = %+v", tasks)
	}
}

func TestCleanupOldImageTasksKeepsCurrentAndActiveTasks(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{ImageRetentionDays: 1}}
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	current := nowISO()
	if err := store.SaveTasks([]ImageTask{
		{ID: "old", Status: "success", UpdatedAt: old},
		{ID: "old-partial", Status: "partial_success", UpdatedAt: old},
		{ID: "active", Status: "running", UpdatedAt: old},
		{ID: "current", Status: "error", UpdatedAt: current},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupOldTasks(); err != nil {
		t.Fatal(err)
	}
	tasks := store.LoadTasks()
	if len(tasks) != 2 || tasks[0].ID != "active" || tasks[1].ID != "current" {
		t.Fatalf("cleanup kept wrong tasks: %+v", tasks)
	}
}

func TestImageTaskQuotaFollowsPersistedTerminalState(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAuthKeys([]UserKey{{
		ID: "owner", Role: "user", Enabled: true,
		ImageDailyQuota: 10, ImageMonthlyQuota: 10, ImageTotalQuota: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTasks([]ImageTask{
		{ID: "canceled", OwnerID: "owner", Status: "canceled"},
		{ID: "failed", OwnerID: "owner", Status: "running"},
		{ID: "finished", OwnerID: "owner", Status: "running"},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	identity := &Identity{ID: "owner", Role: "user"}
	for _, tc := range []struct {
		id, status string
		charged    int
		updated    bool
		used       int
	}{
		{"canceled", "success", 4, false, 0},
		{"failed", "error", 4, true, 0},
		{"finished", "success", 1, true, 1},
	} {
		if !s.consumeImage(identity, tc.charged) {
			t.Fatalf("could not charge quota for %s", tc.id)
		}
		updated, err := s.settleImageTask(ImageTask{ID: tc.id, OwnerID: "owner"}, identity, tc.charged, tc.status, "", nil)
		if err != nil || updated != tc.updated {
			t.Fatalf("settle %s = %t, %v", tc.id, updated, err)
		}
		if used := store.LoadAuthKeys()[0].ImageTotalUsed; used != tc.used {
			t.Fatalf("%s quota used = %d, want %d", tc.id, used, tc.used)
		}
	}
}

func TestProjectImageTaskAddsProgressForLegacyRows(t *testing.T) {
	task := projectImageTask(ImageTask{ID: "legacy", Status: "error", Mode: "generate", Data: []map[string]any{{"url": "one"}}})
	if !task.Terminal || task.RequestedCount != 1 || task.SucceededCount != 1 || task.PendingCount != 0 || task.FailedCount != 0 || task.StageCode != "error" || task.StageLabel != "失败" {
		t.Fatalf("legacy task projection = %+v", task)
	}
	queued := projectImageTask(ImageTask{ID: "queued", Status: "queued", Mode: "generate", RequestedCount: 3})
	if queued.Terminal || queued.PendingCount != 3 || queued.FailedCount != 0 || queued.StageCode != "queued" {
		t.Fatalf("queued task projection = %+v", queued)
	}
	started := time.Now().Add(-1500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	running := projectImageTask(ImageTask{ID: "running", Status: "running", Mode: "generate", RequestedCount: 2, CreatedAt: nowISO(), StartedAt: started})
	if running.Terminal || running.ElapsedMS < 1000 || running.StageCode != "running" {
		t.Fatalf("running task elapsed projection = %+v", running)
	}
	partial := projectImageTask(ImageTask{ID: "partial", Status: "partial_success", Mode: "generate", RequestedCount: 3, Data: []map[string]any{{"url": "one"}, {"url": "two"}}})
	if !partial.Terminal || partial.StageCode != "partial_success" || partial.StageLabel != "部分成功" || partial.SucceededCount != 2 || partial.FailedCount != 1 || partial.PendingCount != 0 {
		t.Fatalf("partial task projection = %+v", partial)
	}
}

func TestPartialImageTaskRefundsOnlyMissingResults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAuthKeys([]UserKey{{
		ID: "owner", Role: "user", Enabled: true,
		ImageDailyQuota: 10, ImageMonthlyQuota: 10, ImageTotalQuota: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	task := ImageTask{ID: "partial", OwnerID: "owner", Status: "running", Mode: "generate", RequestedCount: 3, CreatedAt: nowISO(), UpdatedAt: nowISO()}
	if err := store.SaveTasks([]ImageTask{task}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	identity := &Identity{ID: "owner", Role: "user"}
	if !s.consumeImage(identity, 3) {
		t.Fatal("could not charge image quota")
	}
	data := []map[string]any{{"url": "one"}, {"url": "two"}}
	updated, err := s.settleImageTask(task, identity, 1, "partial_success", "second image failed", data)
	if err != nil || !updated {
		t.Fatalf("settle partial = %t, %v", updated, err)
	}
	keys := store.LoadAuthKeys()
	if len(keys) != 1 || keys[0].ImageTotalUsed != 2 {
		t.Fatalf("partial quota used = %+v, want 2", keys)
	}
	stored := store.LoadTasks()
	if len(stored) != 1 || stored[0].Status != "partial_success" || stored[0].SucceededCount != 2 || stored[0].FailedCount != 1 {
		t.Fatalf("stored partial task = %+v", stored)
	}
}

func TestImageTaskGenerationEarlyFailureReturnsTerminalProjection(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{
		store: store, cfg: Config{AuthKey: "admin-key", SensitiveWords: []string{"blocked"}},
		logSvc: newLogService(filepath.Join(t.TempDir(), "logs")), callStarts: map[string]time.Time{},
		taskCancels: map[imageTaskKey]context.CancelFunc{},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"early-error","prompt":"blocked prompt"}`))
	req.Header.Set("Authorization", "Bearer admin-key")
	response := httptest.NewRecorder()
	s.handleImageTaskGeneration(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var task ImageTask
	if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Status != "error" || !task.Terminal || task.StageCode != "error" || task.PendingCount != 0 || task.FailedCount != 1 {
		t.Fatalf("early failure projection = %+v", task)
	}
	stored := store.LoadTasks()
	if len(stored) != 1 || stored[0].Status != "error" || !stored[0].Terminal || stored[0].StageCode != "error" {
		t.Fatalf("stored early failure = %+v", stored)
	}
}

func TestImageTaskEditMissingInputReturnsTerminalProjection(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{
		store: store, cfg: Config{AuthKey: "admin-key"},
		logSvc: newLogService(filepath.Join(t.TempDir(), "logs")), callStarts: map[string]time.Time{},
		taskCancels: map[imageTaskKey]context.CancelFunc{},
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("client_task_id", "edit-no-input")
	_ = form.WriteField("prompt", "edit this")
	_ = form.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/edits", &body)
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	s.handleImageTaskEdit(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var task ImageTask
	if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Status != "error" || !task.Terminal || task.StageCode != "error" || task.PendingCount != 0 || task.FailedCount != 1 {
		t.Fatalf("edit missing input projection = %+v", task)
	}
}
