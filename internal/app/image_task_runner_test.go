package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestImageTaskRunnerCapacityAndQueuedCancellation(t *testing.T) {
	runner := newImageTaskRunner(1, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var secondRuns, discarded atomic.Int32
	if !runner.reserve() || !runner.submit(imageTaskJob{
		key: imageTaskKey{"owner", "first"},
		run: func() { close(started); <-release },
	}) {
		t.Fatal("first task was not admitted")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	if !runner.reserve() || !runner.submit(imageTaskJob{
		key:       imageTaskKey{"owner", "second"},
		run:       func() { secondRuns.Add(1) },
		onDiscard: func(error) { discarded.Add(1) },
	}) {
		t.Fatal("second task was not queued")
	}
	if runner.reserve() {
		t.Fatal("runner exceeded worker plus queue capacity")
	}
	if !runner.cancelQueued(imageTaskKey{"owner", "second"}) || discarded.Load() != 1 {
		t.Fatal("queued task was not canceled immediately")
	}
	if !runner.reserve() {
		t.Fatal("canceling queued task did not release capacity")
	}
	runner.release()
	close(release)
	runner.close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if secondRuns.Load() != 0 || len(runner.permits) != 0 {
		t.Fatalf("canceled task ran or capacity leaked: runs=%d permits=%d", secondRuns.Load(), len(runner.permits))
	}
}

func TestImageTaskRunnerShutdownDrainsQueuedWork(t *testing.T) {
	runner := newImageTaskRunner(1, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var discarded atomic.Int32
	if !runner.reserve() || !runner.submit(imageTaskJob{run: func() { close(started); <-release }}) {
		t.Fatal("first task was not admitted")
	}
	<-started
	if !runner.reserve() || !runner.submit(imageTaskJob{
		run:       func() { t.Error("queued task ran during shutdown") },
		onDiscard: func(error) { discarded.Add(1) },
	}) {
		t.Fatal("second task was not queued")
	}
	queued := runner.close()
	if len(queued) != 1 || runner.reserve() {
		t.Fatalf("shutdown queue = %d, accepted new work after close", len(queued))
	}
	queued[0].onDiscard(errors.New("shutdown"))
	runner.release()
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if discarded.Load() != 1 || len(runner.permits) != 0 {
		t.Fatalf("shutdown left queued work: discarded=%d permits=%d", discarded.Load(), len(runner.permits))
	}
}

func TestImageTaskInputSpoolIsPrivateAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	spool, err := spoolImageTaskInputs(dir, [][]byte{[]byte("first"), []byte("second")})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(spool)
	info, err := os.Stat(filepath.Join(spool, "0"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("spool permissions = %v, %v", info, err)
	}
	inputs, err := loadImageTaskInputs(spool, 2)
	if err != nil || string(inputs[0]) != "first" || string(inputs[1]) != "second" {
		t.Fatalf("spool round trip = %q, %v", inputs, err)
	}
}

func TestQueuedImageTaskCancellationRefundsAndFreesAdmission(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAuthKeys([]UserKey{{
		ID: "owner", Role: "user", Enabled: true, KeyHash: hashKey("user-key"),
		ImageDailyQuota: 2, ImageMonthlyQuota: 2, ImageTotalQuota: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	runner := newImageTaskRunner(1, 1)
	s := &Server{
		store: store, cfg: Config{AuthKey: "admin-key"},
		logSvc: &logService{store: store}, taskCancels: map[imageTaskKey]context.CancelFunc{},
		imageTasks: runner,
	}
	s.taskRunnerOnce.Do(func() {})
	started := make(chan struct{})
	release := make(chan struct{})
	if !runner.reserve() || !runner.submit(imageTaskJob{run: func() { close(started); <-release }}) {
		t.Fatal("could not occupy worker")
	}
	<-started
	post := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"`+id+`","prompt":"test"}`))
		req.Header.Set("Authorization", "Bearer user-key")
		response := httptest.NewRecorder()
		s.handleImageTaskGeneration(response, req)
		return response
	}
	queued := post("queued")
	if queued.Code != http.StatusOK || len(store.LoadTasks()) != 1 || store.LoadTasks()[0].Status != "queued" {
		t.Fatalf("queued task = %d %s", queued.Code, queued.Body.String())
	}
	if used := store.LoadAuthKeys()[0].ImageTotalUsed; used != 1 {
		t.Fatalf("queued task charged %d, want 1", used)
	}
	full := post("full")
	if full.Code != http.StatusServiceUnavailable || len(store.LoadTasks()) != 1 || store.LoadAuthKeys()[0].ImageTotalUsed != 1 {
		t.Fatalf("full queue left task or quota: %d %s", full.Code, full.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(`{"id":"queued"}`))
	request.Header.Set("Authorization", "Bearer user-key")
	response := httptest.NewRecorder()
	s.handleImageTaskCancel(response, request)
	if response.Code != http.StatusOK || store.LoadTasks()[0].Status != "canceled" || store.LoadAuthKeys()[0].ImageTotalUsed != 0 || len(runner.permits) != 1 {
		t.Fatalf("cancel queued task = %d %s, permits=%d", response.Code, response.Body.String(), len(runner.permits))
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.ShutdownImageTasks(ctx); err != nil {
		t.Fatal(err)
	}
}
