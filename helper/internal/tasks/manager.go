package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
)

// NotFoundError identifies an operation attempted on an unknown task.
type NotFoundError struct {
	ID string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("task %q not found", e.ID)
}

// TransitionError identifies an operation that is not valid for a task's
// current lifecycle state.
type TransitionError struct {
	ID   string
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("task %q cannot transition from %q to %q", e.ID, e.From, e.To)
}

// ProgressError identifies an invalid progress update.
type ProgressError struct {
	ID       string
	Current  float64
	Proposed float64
	Reason   string
}

func (e *ProgressError) Error() string {
	return fmt.Sprintf("task %q progress cannot change from %v to %v: %s", e.ID, e.Current, e.Proposed, e.Reason)
}

type record struct {
	task          Task
	ctx           context.Context
	cancel        context.CancelFunc
	stopWatcher   func() bool
	internalError error
}

// Manager stores task attempts in memory and protects them for concurrent use.
type Manager struct {
	mu      sync.RWMutex
	records map[string]*record
	order   []string
}

func NewManager() *Manager {
	return &Manager{records: make(map[string]*record)}
}

// Create adds a new queued task attempt.
func (m *Manager) Create(url, title string) (Task, error) {
	return m.CreateWithContext(context.Background(), url, title)
}

// CreateWithContext adds a queued task tied to a parent context. Canceling the
// parent cancels queued and active attempts, but never overwrites a terminal
// state. context.AfterFunc avoids one waiting goroutine per task.
func (m *Manager) CreateWithContext(parent context.Context, url, title string) (Task, error) {
	if parent == nil {
		return Task{}, fmt.Errorf("create task: nil parent context")
	}
	id, err := newID()
	if err != nil {
		return Task{}, fmt.Errorf("generate task ID: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	record := &record{
		task: Task{
			ID:     id,
			URL:    url,
			Title:  title,
			Status: Queued,
		},
		ctx:    ctx,
		cancel: cancel,
	}

	m.mu.Lock()
	m.records[id] = record
	m.order = append(m.order, id)
	if ctx.Err() != nil {
		record.task.Status = Canceled
		releaseContextLocked(record)
	} else {
		record.stopWatcher = context.AfterFunc(ctx, func() {
			m.cancelFromContext(id)
		})
	}
	task := record.task
	m.mu.Unlock()
	return task, nil
}

// Get returns a copy of a task.
func (m *Manager) Get(id string) (Task, error) {
	m.mu.RLock()
	record, ok := m.records[id]
	if !ok {
		m.mu.RUnlock()
		return Task{}, &NotFoundError{ID: id}
	}
	task := record.task
	m.mu.RUnlock()
	return task, nil
}

// List returns task copies in creation order.
func (m *Manager) List() []Task {
	m.mu.RLock()
	tasks := make([]Task, 0, len(m.order))
	for _, id := range m.order {
		tasks = append(tasks, m.records[id].task)
	}
	m.mu.RUnlock()
	return tasks
}

// Context returns the cancellation context for a task attempt.
func (m *Manager) Context(id string) (context.Context, error) {
	m.mu.RLock()
	record, ok := m.records[id]
	if !ok {
		m.mu.RUnlock()
		return nil, &NotFoundError{ID: id}
	}
	ctx := record.ctx
	m.mu.RUnlock()
	return ctx, nil
}

// Transition advances a task through its non-terminal lifecycle states.
func (m *Manager) Transition(id string, to Status) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.recordLocked(id)
	if err != nil {
		return Task{}, err
	}
	syncCanceledLocked(record)
	if !validTransition(record.task.Status, to) {
		return Task{}, &TransitionError{ID: id, From: record.task.Status, To: to}
	}
	record.task.Status = to
	return record.task, nil
}

// SetProgress records a validated progress percentage. Progress is restricted
// to active tasks, must be in [0, 100], and cannot move backwards.
func (m *Manager) SetProgress(id string, progress float64) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.recordLocked(id)
	if err != nil {
		return Task{}, err
	}
	syncCanceledLocked(record)
	if record.task.Status != Downloading && record.task.Status != Merging {
		return Task{}, &ProgressError{
			ID: id, Current: record.task.Progress, Proposed: progress,
			Reason: "task is not active",
		}
	}
	if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 || progress > 100 {
		return Task{}, &ProgressError{
			ID: id, Current: record.task.Progress, Proposed: progress,
			Reason: "value must be between 0 and 100",
		}
	}
	if progress < record.task.Progress {
		return Task{}, &ProgressError{
			ID: id, Current: record.task.Progress, Proposed: progress,
			Reason: "value must not decrease",
		}
	}
	record.task.Progress = progress
	return record.task, nil
}

// Complete marks a downloading or merging task completed. Direct downloads
// may complete without entering the merging state.
func (m *Manager) Complete(id, outputPath string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.recordLocked(id)
	if err != nil {
		return Task{}, err
	}
	syncCanceledLocked(record)
	if record.task.Status != Downloading && record.task.Status != Merging {
		return Task{}, &TransitionError{ID: id, From: record.task.Status, To: Completed}
	}
	record.task.Status = Completed
	record.task.Progress = 100
	record.task.OutputPath = outputPath
	releaseContextLocked(record)
	return record.task, nil
}

// CompletePublished records an output that was published before cancellation
// won the task-state race. Active and canceled tasks may be completed this way.
func (m *Manager) CompletePublished(id, outputPath string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.recordLocked(id)
	if err != nil {
		return Task{}, err
	}
	syncCanceledLocked(record)
	if record.task.Status != Downloading && record.task.Status != Merging && record.task.Status != Canceled {
		return Task{}, &TransitionError{ID: id, From: record.task.Status, To: Completed}
	}
	record.task.Status = Completed
	record.task.Progress = 100
	record.task.OutputPath = outputPath
	record.task.Error = ""
	record.task.ErrorCode = ""
	record.internalError = nil
	releaseContextLocked(record)
	return record.task, nil
}

// Fail marks a non-terminal task failed. Only the user-facing message is kept
// in Task; the internal diagnostic remains outside the serialized model.
func (m *Manager) Fail(id, userMessage string, internal error) (Task, error) {
	return m.FailWithCode(id, "", userMessage, internal)
}

// FailWithCode marks a non-terminal task failed with a safe, machine-readable
// code. Internal diagnostics remain outside the serialized model.
func (m *Manager) FailWithCode(id, code, userMessage string, internal error) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.recordLocked(id)
	if err != nil {
		return Task{}, err
	}
	syncCanceledLocked(record)
	if isTerminal(record.task.Status) {
		return Task{}, &TransitionError{ID: id, From: record.task.Status, To: Failed}
	}
	record.task.Status = Failed
	record.task.Error = userMessage
	record.task.ErrorCode = code
	record.internalError = internal
	releaseContextLocked(record)
	return record.task, nil
}

// Cancel marks an active or queued task canceled and signals its context.
// Cancel is idempotent for an already canceled task.
func (m *Manager) Cancel(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.recordLocked(id)
	if err != nil {
		return Task{}, err
	}
	if record.task.Status == Canceled {
		return record.task, nil
	}
	if isTerminal(record.task.Status) {
		return Task{}, &TransitionError{ID: id, From: record.task.Status, To: Canceled}
	}
	record.task.Status = Canceled
	releaseContextLocked(record)
	return record.task, nil
}

// Retry creates an independent queued attempt for a failed or canceled task.
// The source URL and title are preserved; progress and result fields are reset.
func (m *Manager) Retry(id string) (Task, error) {
	m.mu.Lock()
	record, ok := m.records[id]
	if !ok {
		m.mu.Unlock()
		return Task{}, &NotFoundError{ID: id}
	}
	syncCanceledLocked(record)
	if record.task.Status != Failed && record.task.Status != Canceled {
		from := record.task.Status
		m.mu.Unlock()
		return Task{}, &TransitionError{ID: id, From: from, To: Queued}
	}
	url, title := record.task.URL, record.task.Title
	m.mu.Unlock()

	return m.Create(url, title)
}

func (m *Manager) internalError(id string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.records[id]
	if !ok {
		return &NotFoundError{ID: id}
	}
	return record.internalError
}

func (m *Manager) cancelFromContext(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok || isTerminal(record.task.Status) {
		return
	}
	record.task.Status = Canceled
	releaseContextLocked(record)
}

func (m *Manager) recordLocked(id string) (*record, error) {
	record, ok := m.records[id]
	if !ok {
		return nil, &NotFoundError{ID: id}
	}
	return record, nil
}

func validTransition(from, to Status) bool {
	return (from == Queued && to == Downloading) ||
		(from == Downloading && to == Merging)
}

func isTerminal(status Status) bool {
	return status == Completed || status == Failed || status == Canceled
}

func syncCanceledLocked(record *record) {
	if !isTerminal(record.task.Status) && record.ctx.Err() != nil {
		record.task.Status = Canceled
		releaseContextLocked(record)
	}
}

func releaseContextLocked(record *record) {
	if record.stopWatcher != nil {
		record.stopWatcher()
		record.stopWatcher = nil
	}
	if record.cancel != nil {
		record.cancel()
		record.cancel = nil
	}
}

func newID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}
