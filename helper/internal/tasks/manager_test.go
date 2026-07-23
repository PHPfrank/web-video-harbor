package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func TestTaskLifecycleCompletes(t *testing.T) {
	m := NewManager()
	task := mustCreate(t, m, "https://media.example/video/master.m3u8", "示例视频")

	assertTaskState(t, task, Queued, 0)
	task = mustTransition(t, m, task.ID, Downloading)
	task = mustProgress(t, m, task.ID, 62.5)
	assertTaskState(t, task, Downloading, 62.5)
	task = mustTransition(t, m, task.ID, Merging)
	task = mustProgress(t, m, task.ID, 94)
	task, err := m.Complete(task.ID, "/Users/test/Downloads/示例视频.mp4")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	assertTaskState(t, task, Completed, 100)
	if task.OutputPath != "/Users/test/Downloads/示例视频.mp4" {
		t.Fatalf("OutputPath = %q", task.OutputPath)
	}
}

func TestFailKeepsInternalDetailOutOfJSON(t *testing.T) {
	m := NewManager()
	task := mustCreate(t, m, "https://media.example/video.mp4", "视频")
	task = mustTransition(t, m, task.ID, Downloading)
	internal := errors.New("upstream returned token=secret-value")

	task, err := m.Fail(task.ID, "视频下载失败，请稍后重试", internal)
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if task.Status != Failed || task.Error != "视频下载失败，请稍后重试" {
		t.Fatalf("failed task = %#v", task)
	}
	if !errors.Is(m.internalError(task.ID), internal) {
		t.Fatalf("internalError() did not retain original error")
	}

	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if got := string(encoded); contains(got, "secret-value") || contains(got, "token=") {
		t.Fatalf("JSON leaked internal detail: %s", got)
	}
}

func TestCancelQueuedAndActiveTasksCancelsContext(t *testing.T) {
	for _, initial := range []Status{Queued, Downloading, Merging} {
		t.Run(string(initial), func(t *testing.T) {
			m := NewManager()
			task := mustCreate(t, m, "https://media.example/video.mp4", "视频")
			if initial != Queued {
				task = mustTransition(t, m, task.ID, Downloading)
			}
			if initial == Merging {
				task = mustTransition(t, m, task.ID, Merging)
			}
			ctx, err := m.Context(task.ID)
			if err != nil {
				t.Fatalf("Context() error = %v", err)
			}

			canceled, err := m.Cancel(task.ID)
			if err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
			if canceled.Status != Canceled {
				t.Fatalf("Status = %q, want %q", canceled.Status, Canceled)
			}
			select {
			case <-ctx.Done():
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("context error = %v", ctx.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("task context was not canceled")
			}

			again, err := m.Cancel(task.ID)
			if err != nil {
				t.Fatalf("second Cancel() error = %v", err)
			}
			if again.Status != Canceled {
				t.Fatalf("second Cancel() status = %q", again.Status)
			}
		})
	}
}

func TestParentContextCancellationMovesQueuedAndActiveTasksToCanceled(t *testing.T) {
	for _, initial := range []Status{Queued, Downloading, Merging} {
		t.Run(string(initial), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			m := NewManager()
			task, err := m.CreateWithContext(parent, "https://media.example/video.mp4", "视频")
			if err != nil {
				t.Fatalf("CreateWithContext() error = %v", err)
			}
			if initial != Queued {
				task = mustTransition(t, m, task.ID, Downloading)
			}
			if initial == Merging {
				task = mustTransition(t, m, task.ID, Merging)
			}

			cancel()
			waitForStatus(t, m, task.ID, Canceled)
			if _, err := m.Cancel(task.ID); err != nil {
				t.Fatalf("Cancel() after parent cancellation error = %v", err)
			}
		})
	}
}

func TestCanceledParentWinsBeforeAnyNonCancelMutation(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T, *Manager, Task) Task
		operation func(*Manager, string) error
	}{
		{
			name:    "transition",
			prepare: func(_ *testing.T, _ *Manager, task Task) Task { return task },
			operation: func(m *Manager, id string) error {
				_, err := m.Transition(id, Downloading)
				return err
			},
		},
		{
			name: "progress",
			prepare: func(t *testing.T, m *Manager, task Task) Task {
				return mustTransition(t, m, task.ID, Downloading)
			},
			operation: func(m *Manager, id string) error {
				_, err := m.SetProgress(id, 25)
				return err
			},
		},
		{
			name: "complete",
			prepare: func(t *testing.T, m *Manager, task Task) Task {
				return mustTransition(t, m, task.ID, Downloading)
			},
			operation: func(m *Manager, id string) error {
				_, err := m.Complete(id, "/tmp/video.mp4")
				return err
			},
		},
		{
			name:    "fail",
			prepare: func(_ *testing.T, _ *Manager, task Task) Task { return task },
			operation: func(m *Manager, id string) error {
				_, err := m.Fail(id, "下载失败", errors.New("diagnostic"))
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			m := NewManager()
			task, err := m.CreateWithContext(parent, "https://media.example/video.mp4", "视频")
			if err != nil {
				t.Fatalf("CreateWithContext() error = %v", err)
			}
			task = tt.prepare(t, m, task)

			// Disable the asynchronous observer to deterministically exercise the
			// mutation's own canceled-context check.
			m.mu.Lock()
			if !m.records[task.ID].stopWatcher() {
				m.mu.Unlock()
				t.Fatal("cancellation observer had already started")
			}
			m.mu.Unlock()
			cancel()

			if err := tt.operation(m, task.ID); err == nil {
				t.Fatal("mutation succeeded after parent context cancellation")
			}
			got, err := m.Get(task.ID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.Status != Canceled {
				t.Fatalf("Status = %q, want %q", got.Status, Canceled)
			}
			if got.Progress != task.Progress || got.OutputPath != "" || got.Error != "" {
				t.Fatalf("canceled task was mutated: %#v", got)
			}
		})
	}
}

func TestCreateWithCanceledParentReturnsCanceledTask(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	m := NewManager()
	task, err := m.CreateWithContext(parent, "https://media.example/video.mp4", "视频")
	if err != nil {
		t.Fatalf("CreateWithContext() error = %v", err)
	}
	if task.Status != Canceled {
		t.Fatalf("CreateWithContext() status = %q, want %q", task.Status, Canceled)
	}
	got, err := m.Get(task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != Canceled {
		t.Fatalf("stored status = %q, want %q", got.Status, Canceled)
	}
}

func TestParentContextCancellationDoesNotOverwriteTerminalState(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	m := NewManager()
	task, err := m.CreateWithContext(parent, "https://media.example/video.mp4", "视频")
	if err != nil {
		t.Fatalf("CreateWithContext() error = %v", err)
	}
	task = mustTransition(t, m, task.ID, Downloading)
	if _, err := m.Complete(task.ID, "/tmp/video.mp4"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	cancel()
	got, err := m.Get(task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != Completed {
		t.Fatalf("parent cancellation overwrote terminal status with %q", got.Status)
	}
}

func TestRetryCreatesFreshAttemptForFailedOrCanceledTask(t *testing.T) {
	for _, terminal := range []Status{Failed, Canceled} {
		t.Run(string(terminal), func(t *testing.T) {
			m := NewManager()
			original := mustCreate(t, m, "https://media.example/master.m3u8", "原始标题")
			if terminal == Failed {
				var err error
				original, err = m.Fail(original.ID, "失败", errors.New("diagnostic"))
				if err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			} else {
				var err error
				original, err = m.Cancel(original.ID)
				if err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			}

			retry, err := m.Retry(original.ID)
			if err != nil {
				t.Fatalf("Retry() error = %v", err)
			}
			if retry.ID == original.ID {
				t.Fatal("Retry() reused the original task ID")
			}
			if retry.URL != original.URL || retry.Title != original.Title {
				t.Fatalf("retry source = (%q, %q), want (%q, %q)", retry.URL, retry.Title, original.URL, original.Title)
			}
			if retry.Status != Queued || retry.Progress != 0 || retry.Error != "" || retry.OutputPath != "" {
				t.Fatalf("retry was not a fresh attempt: %#v", retry)
			}
			gotOriginal, err := m.Get(original.ID)
			if err != nil {
				t.Fatalf("Get(original) error = %v", err)
			}
			if gotOriginal.Status != terminal {
				t.Fatalf("retry mutated original status to %q", gotOriginal.Status)
			}
		})
	}
}

func TestRetryRejectsActiveAndCompletedTasks(t *testing.T) {
	for _, status := range []Status{Queued, Downloading, Merging, Completed} {
		t.Run(string(status), func(t *testing.T) {
			m := NewManager()
			task := mustCreate(t, m, "https://media.example/video.mp4", "视频")
			if status != Queued {
				task = mustTransition(t, m, task.ID, Downloading)
			}
			if status == Merging || status == Completed {
				task = mustTransition(t, m, task.ID, Merging)
			}
			if status == Completed {
				var err error
				task, err = m.Complete(task.ID, "/tmp/video.mp4")
				if err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			}

			_, err := m.Retry(task.ID)
			var transitionErr *TransitionError
			if !errors.As(err, &transitionErr) {
				t.Fatalf("Retry() error = %v, want *TransitionError", err)
			}
		})
	}
}

func TestInvalidTransitionsAreRejected(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
	}{
		{Queued, Merging},
		{Queued, Completed},
		{Downloading, Queued},
		{Merging, Downloading},
		{Failed, Downloading},
		{Canceled, Downloading},
		{Completed, Downloading},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_to_%s", tt.from, tt.to), func(t *testing.T) {
			m := NewManager()
			task := taskInState(t, m, tt.from)
			_, err := m.Transition(task.ID, tt.to)
			var transitionErr *TransitionError
			if !errors.As(err, &transitionErr) {
				t.Fatalf("Transition() error = %v, want *TransitionError", err)
			}
			unchanged, getErr := m.Get(task.ID)
			if getErr != nil {
				t.Fatalf("Get() error = %v", getErr)
			}
			if unchanged.Status != tt.from {
				t.Fatalf("invalid transition changed status to %q", unchanged.Status)
			}
		})
	}
}

func TestProgressMustBeMonotonicAndWithinRange(t *testing.T) {
	m := NewManager()
	task := mustCreate(t, m, "https://media.example/video.mp4", "视频")
	task = mustTransition(t, m, task.ID, Downloading)
	task = mustProgress(t, m, task.ID, 55)

	for _, progress := range []float64{-1, 54.9, 101, math.NaN()} {
		if _, err := m.SetProgress(task.ID, progress); err == nil {
			t.Fatalf("SetProgress(%v) error = nil", progress)
		}
		unchanged, err := m.Get(task.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if unchanged.Progress != 55 {
			t.Fatalf("invalid progress changed value to %v", unchanged.Progress)
		}
	}
}

func TestTerminalStateIsImmutable(t *testing.T) {
	for _, terminal := range []Status{Completed, Failed, Canceled} {
		t.Run(string(terminal), func(t *testing.T) {
			m := NewManager()
			task := taskInState(t, m, terminal)
			if _, err := m.SetProgress(task.ID, 99); err == nil {
				t.Fatal("SetProgress() changed a terminal task")
			}
			if _, err := m.Fail(task.ID, "other", errors.New("other")); err == nil {
				t.Fatal("Fail() changed a terminal task")
			}
			if terminal != Canceled {
				if _, err := m.Cancel(task.ID); err == nil {
					t.Fatal("Cancel() changed a terminal task")
				}
			}
		})
	}
}

func TestGetAndListReturnCopies(t *testing.T) {
	m := NewManager()
	created := mustCreate(t, m, "https://media.example/video.mp4", "原始标题")

	got, err := m.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	got.Title = "被修改"
	listed := m.List()
	if len(listed) != 1 {
		t.Fatalf("List() length = %d, want 1", len(listed))
	}
	listed[0].URL = "https://attacker.example/changed"

	again, err := m.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if again.Title != "原始标题" || again.URL != "https://media.example/video.mp4" {
		t.Fatalf("caller mutation altered manager state: %#v", again)
	}
}

func TestConcurrentMutationsListAndGet(t *testing.T) {
	m := NewManager()
	start := make(chan struct{})
	done := make(chan struct{})
	errorsFound := make(chan error, 32)

	var writers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		writers.Add(1)
		go func(worker int) {
			defer writers.Done()
			<-start
			for round := 0; round < 40; round++ {
				task, err := m.Create(
					fmt.Sprintf("https://media.example/%d/%d.mp4", worker, round),
					fmt.Sprintf("视频 %d-%d", worker, round),
				)
				if err != nil {
					errorsFound <- err
					return
				}
				if _, err = m.Transition(task.ID, Downloading); err != nil {
					errorsFound <- err
					return
				}
				if _, err = m.SetProgress(task.ID, 50); err != nil {
					errorsFound <- err
					return
				}
				if round%2 == 0 {
					_, err = m.Cancel(task.ID)
				} else {
					_, err = m.Complete(task.ID, "/tmp/video.mp4")
				}
				if err != nil {
					errorsFound <- err
					return
				}
			}
		}(worker)
	}

	var readers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for {
				select {
				case <-done:
					return
				default:
				}
				for _, task := range m.List() {
					if _, err := m.Get(task.ID); err != nil {
						errorsFound <- err
						return
					}
				}
			}
		}()
	}

	close(start)
	writers.Wait()
	close(done)
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent manager operation error = %v", err)
	}
}

func TestUnknownIDReturnsTypedNotFoundError(t *testing.T) {
	m := NewManager()
	operations := []func() error{
		func() error { _, err := m.Get("missing"); return err },
		func() error { _, err := m.Context("missing"); return err },
		func() error { _, err := m.Transition("missing", Downloading); return err },
		func() error { _, err := m.SetProgress("missing", 1); return err },
		func() error { _, err := m.Fail("missing", "失败", errors.New("detail")); return err },
		func() error { _, err := m.Cancel("missing"); return err },
		func() error { _, err := m.Retry("missing"); return err },
	}

	for i, operation := range operations {
		err := operation()
		var notFound *NotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("operation %d error = %v, want *NotFoundError", i, err)
		}
		if notFound.ID != "missing" || err.Error() != `task "missing" not found` {
			t.Fatalf("operation %d returned unstable error: %#v / %q", i, notFound, err)
		}
	}
}

func mustCreate(t *testing.T, m *Manager, url, title string) Task {
	t.Helper()
	task, err := m.Create(url, title)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return task
}

func mustTransition(t *testing.T, m *Manager, id string, status Status) Task {
	t.Helper()
	task, err := m.Transition(id, status)
	if err != nil {
		t.Fatalf("Transition(%q) error = %v", status, err)
	}
	return task
}

func mustProgress(t *testing.T, m *Manager, id string, progress float64) Task {
	t.Helper()
	task, err := m.SetProgress(id, progress)
	if err != nil {
		t.Fatalf("SetProgress(%v) error = %v", progress, err)
	}
	return task
}

func assertTaskState(t *testing.T, task Task, status Status, progress float64) {
	t.Helper()
	if task.Status != status || task.Progress != progress {
		t.Fatalf("task state = (%q, %v), want (%q, %v)", task.Status, task.Progress, status, progress)
	}
}

func taskInState(t *testing.T, m *Manager, status Status) Task {
	t.Helper()
	task := mustCreate(t, m, "https://media.example/video.mp4", "视频")
	switch status {
	case Queued:
		return task
	case Downloading:
		return mustTransition(t, m, task.ID, Downloading)
	case Merging:
		task = mustTransition(t, m, task.ID, Downloading)
		return mustTransition(t, m, task.ID, Merging)
	case Completed:
		task = mustTransition(t, m, task.ID, Downloading)
		task = mustTransition(t, m, task.ID, Merging)
		completed, err := m.Complete(task.ID, "/tmp/video.mp4")
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		return completed
	case Failed:
		failed, err := m.Fail(task.ID, "失败", errors.New("detail"))
		if err != nil {
			t.Fatalf("Fail() error = %v", err)
		}
		return failed
	case Canceled:
		canceled, err := m.Cancel(task.ID)
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		return canceled
	default:
		t.Fatalf("unsupported test state %q", status)
		return Task{}
	}
}

func contains(s, substring string) bool {
	for i := 0; i+len(substring) <= len(s); i++ {
		if s[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

func waitForStatus(t *testing.T, m *Manager, id string, want Status) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := m.Get(id)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if task.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task did not reach status %q", want)
}
