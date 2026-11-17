package task

import "time"

// CheckpointVersion is the shape of a saved Checkpoint. A continuation refuses
// a checkpoint it cannot read instead of guessing what a field means; bump this
// whenever an existing field changes meaning.
const CheckpointVersion = 1

// Checkpoint is the progress a paused run resumes from: the last safe boundary
// plus the routing that produced it. It records boundaries, not conversation —
// the child session keeps the messages, and this only says where the next
// execution continues.
type Checkpoint struct {
	Version   int    `json:"version"`
	SessionID string `json:"sessionId,omitempty"`
	// Attempt is the execution this checkpoint belongs to, so a retired
	// execution cannot overwrite the progress of the one that replaced it.
	Attempt int `json:"attempt"`
	// Steps counts completed tool calls: the next execution starts after this
	// many, rather than repeating the last one.
	Steps        int       `json:"steps"`
	Model        string    `json:"model,omitempty"`
	Effort       string    `json:"effort,omitempty"`
	Tier         string    `json:"tier,omitempty"`
	Cwd          string    `json:"cwd,omitempty"`
	LastToolCall string    `json:"lastToolCall,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// SetCheckpoint records progress at a safe boundary. A checkpoint is accepted
// only while the task runs, and only for the current attempt: a retired
// execution must not move the boundary the live one is working from.
func (t *AgentTask) SetCheckpoint(cp Checkpoint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status != StatusRunning {
		return
	}
	cp.Version = CheckpointVersion
	cp.Attempt = t.attempt
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = time.Now()
	}
	t.checkpoint = &cp
}

// Checkpoint returns the last recorded progress and whether there is one.
func (t *AgentTask) Checkpoint() (Checkpoint, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.checkpoint == nil {
		return Checkpoint{}, false
	}
	return *t.checkpoint, true
}

// Paused reports whether the task stopped at a checkpoint on a retryable
// failure, so a continuation can pick the work up instead of redoing it.
func (t *AgentTask) Paused() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status == StatusPaused
}
