package app

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var errImageTaskQueueFull = errors.New("图片任务队列已满，请稍后重试")

const defaultImageTaskWorkers = 16
const defaultImageTaskQueueSize = 256

type imageTaskJob struct {
	key       imageTaskKey
	run       func()
	onDiscard func(error)
}

type imageTaskRunner struct {
	mu      sync.Mutex
	jobs    []imageTaskJob
	ready   *sync.Cond
	permits chan struct{}
	workers int
	started bool
	closed  bool
	waiters sync.WaitGroup
}

func imageTaskEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func newImageTaskRunner(workers, queueSize int) *imageTaskRunner {
	workers = max(1, workers)
	queueSize = max(1, queueSize)
	runner := &imageTaskRunner{
		permits: make(chan struct{}, workers+queueSize),
		workers: workers,
	}
	runner.ready = sync.NewCond(&runner.mu)
	return runner
}

func (r *imageTaskRunner) reserve() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	select {
	case r.permits <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *imageTaskRunner) release() { <-r.permits }

func (r *imageTaskRunner) submit(job imageTaskJob) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	if !r.started {
		r.started = true
		for range r.workers {
			r.waiters.Add(1)
			go r.worker()
		}
	}
	r.jobs = append(r.jobs, job)
	r.ready.Signal()
	return true
}

func (r *imageTaskRunner) worker() {
	defer r.waiters.Done()
	for {
		r.mu.Lock()
		for len(r.jobs) == 0 && !r.closed {
			r.ready.Wait()
		}
		if r.closed {
			r.mu.Unlock()
			return
		}
		job := r.jobs[0]
		r.jobs[0] = imageTaskJob{}
		r.jobs = r.jobs[1:]
		r.mu.Unlock()
		r.run(job)
	}
}

func (r *imageTaskRunner) cancelQueued(key imageTaskKey) bool {
	r.mu.Lock()
	var canceled imageTaskJob
	for index, job := range r.jobs {
		if job.key == key {
			canceled = job
			copy(r.jobs[index:], r.jobs[index+1:])
			r.jobs[len(r.jobs)-1] = imageTaskJob{}
			r.jobs = r.jobs[:len(r.jobs)-1]
			break
		}
	}
	r.mu.Unlock()
	if canceled.run == nil {
		return false
	}
	if canceled.onDiscard != nil {
		canceled.onDiscard(errors.New("canceled"))
	}
	r.release()
	return true
}

func (r *imageTaskRunner) run(job imageTaskJob) {
	defer r.release()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("image task worker panic: %v", recovered)
			if job.onDiscard != nil {
				job.onDiscard(errors.New("image task worker failed"))
			}
		}
	}()
	job.run()
}

func (r *imageTaskRunner) close() []imageTaskJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	queued := r.jobs
	r.jobs = nil
	r.ready.Broadcast()
	return queued
}

func (r *imageTaskRunner) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.waiters.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *imageTaskRunner) status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]any{
		"workers":  r.workers,
		"capacity": cap(r.permits),
		"accepted": len(r.permits),
		"queued":   len(r.jobs),
		"started":  r.started,
		"closed":   r.closed,
	}
}

func (s *Server) taskRunner() *imageTaskRunner {
	s.taskRunnerOnce.Do(func() {
		s.imageTasks = newImageTaskRunner(
			imageTaskEnvInt("CHATGPT2API_IMAGE_TASK_WORKERS", defaultImageTaskWorkers),
			imageTaskEnvInt("CHATGPT2API_IMAGE_TASK_QUEUE_SIZE", defaultImageTaskQueueSize),
		)
	})
	return s.imageTasks
}

func (s *Server) imageTaskRunnerStatus() map[string]any {
	if s.imageTasks == nil {
		return map[string]any{"started": false, "closed": false}
	}
	return s.imageTasks.status()
}

func (s *Server) ShutdownImageTasks(ctx context.Context) error {
	if s.imageTasks == nil {
		return nil
	}
	queued := s.imageTasks.close()
	s.taskMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.taskCancels))
	for _, cancel := range s.taskCancels {
		cancels = append(cancels, cancel)
	}
	s.taskMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, job := range queued {
		if job.onDiscard != nil {
			job.onDiscard(errors.New("image task interrupted before start"))
		}
		s.imageTasks.release()
	}
	return s.imageTasks.wait(ctx)
}

func (s *Server) admitImageTask(task ImageTask) (ImageTask, bool, *imageTaskRunner, error) {
	for _, existing := range s.store.LoadTasks() {
		if existing.OwnerID == task.OwnerID && existing.ID == task.ID {
			return existing, false, nil, nil
		}
	}
	runner := s.taskRunner()
	if !runner.reserve() {
		return ImageTask{}, false, nil, errImageTaskQueueFull
	}
	existing, created, err := s.insertTask(task)
	if err != nil || !created {
		runner.release()
		return existing, created, nil, err
	}
	return task, true, runner, nil
}

func spoolImageTaskInputs(dir string, inputs [][]byte) (string, error) {
	spoolDir, err := os.MkdirTemp(dir, "image-task-spool-")
	if err != nil {
		return "", err
	}
	for index, data := range inputs {
		name := filepath.Join(spoolDir, strconv.Itoa(index))
		if err := os.WriteFile(name, data, 0600); err != nil {
			os.RemoveAll(spoolDir)
			return "", err
		}
	}
	return spoolDir, nil
}

func loadImageTaskInputs(spoolDir string, count int) ([][]byte, error) {
	inputs := make([][]byte, 0, count)
	for index := range count {
		data, err := os.ReadFile(filepath.Join(spoolDir, strconv.Itoa(index)))
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, data)
	}
	return inputs, nil
}
