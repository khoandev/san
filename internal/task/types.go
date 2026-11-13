package task

import (
	"time"
)

// TaskType represents the type of background task
type TaskType string

const (
	TaskTypeBash  TaskType = "bash"
	TaskTypeAgent TaskType = "agent"
)

// TaskStatus represents the status of a background task
type TaskStatus string

const (
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusKilled    TaskStatus = "killed"
	// StatusStopped marks a task whose run was cancelled (user stop, shutdown)
	// rather than failing on its own. Kept distinct from StatusFailed so the
	// main agent does not treat a deliberate stop as an error to retry.
	StatusStopped TaskStatus = "stopped"
)

// Brief is the display slice of a task: identity, lifecycle, and the activity a
// tracker row names — everything a renderer reads, and none of the output.
//
// A TaskInfo carries the retained output (up to 512 KiB per task) and a copy of
// the attempt rows. A renderer runs several times per frame, so anything that
// only draws a task asks for a Brief: today's compiler elides the unused output
// copy as long as nothing retains it, and a Brief keeps a renderer from
// depending on that.
type Brief struct {
	ID              string
	ParentSessionID string
	AgentSessionID  string
	Type            TaskType
	Status          TaskStatus
	Attempt         int

	// Display-only detail: what a tracker row prints beside the identity.
	AgentName   string
	Description string
	Error       string
	OutputError string // First output-log failure; a nonempty path alone is not proof of durability.
	Progress    string
	LiveView    string
	StartTime   time.Time
	EndTime     time.Time
}

// Briefer is implemented by task types that can answer without copying output.
type Briefer interface {
	Brief() Brief
}

// Previewer is implemented by task types that can answer a roster's read
// without copying more retained output than the row can show.
type Previewer interface {
	Preview(limit int) TaskInfo
}

// PreviewOf reads a task's status with Output bounded to limit trailing bytes.
// A task that does not opt in pays the full copy, so a renderer still gets a
// correct row.
func PreviewOf(worker BackgroundTask, limit int) TaskInfo {
	if previewer, ok := worker.(Previewer); ok {
		return previewer.Preview(limit)
	}
	info := worker.GetStatus()
	info.Output, info.OutputTruncated = boundOutput(info.Output, limit)
	return info
}

// BriefOf reads a task's display slice without copying its retained output,
// falling back to a full status read for implementations that do not opt in.
func BriefOf(worker BackgroundTask) Brief {
	if briefer, ok := worker.(Briefer); ok {
		return briefer.Brief()
	}
	info := worker.GetStatus()
	return Brief{
		ID: info.ID, ParentSessionID: info.ParentSessionID, AgentSessionID: info.AgentSessionID,
		Type: info.Type, Status: info.Status, Attempt: info.Attempt,
		AgentName: info.AgentName, Description: info.Description,
		Error: info.Error, OutputError: info.OutputError,
		Progress: info.Progress, LiveView: info.LiveView,
		StartTime: info.StartTime, EndTime: info.EndTime,
	}
}

// BackgroundTask is the common interface for all background task types
// Both BashTask and AgentTask implement this interface
//
// An implementation owes one thing the compiler cannot check: a Stop-induced
// exit must stay distinguishable from a failure, so that a task the user
// called off is not reported as broken work to retry. How is left open,
// because the two existing types genuinely differ — a bash child reports its
// graceful SIGTERM, while an agent goroutine returns context.Canceled.
type BackgroundTask interface {
	// GetID returns the unique task identifier
	GetID() string

	// GetType returns the task type (bash or agent)
	GetType() TaskType

	// GetDescription returns a brief description of the task
	GetDescription() string

	// GetStatus returns the current task status info
	GetStatus() TaskInfo

	// IsRunning returns true if the task is still running
	IsRunning() bool

	// WaitForCompletion waits until the task completes or timeout
	// Returns true if completed, false if timeout
	WaitForCompletion(timeout time.Duration) bool

	// Stop asks the task to exit on its own and returns without waiting for
	// it to. Escalation is the caller's job, not the implementer's:
	// Manager.Kill waits gracefulStopTimeout and then calls Kill, so a Stop
	// must not enforce a deadline of its own.
	//
	// An implementation must not let its own hard-kill path pre-empt the
	// graceful attempt it is making here — a Stop that trips the very
	// mechanism that kills the task outright has not stopped it gracefully at
	// all, it has just taken a longer route to Kill.
	Stop() error

	// Kill forcefully terminates the task (SIGKILL for bash). It is safe to
	// call after Stop, and after the task has already ended.
	Kill() error

	// AppendOutput appends data to the output buffer
	AppendOutput(data []byte)

	// GetOutput returns the current output
	GetOutput() string
}

// FailureKind records observed runtime evidence, not a guess based on error prose.
type FailureKind string

const (
	FailureUnknown FailureKind = "unknown"
	FailureTimeout FailureKind = "timeout"
	FailureExit    FailureKind = "exit"
	FailureSignal  FailureKind = "signal"
	FailureAgent   FailureKind = "agent_error"
)

type FailureInfo struct {
	Kind    FailureKind   `json:"kind,omitempty"`
	Signal  string        `json:"signal,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Lifetime distinguishes finite jobs from explicitly session-owned services.
type Lifetime string

const (
	LifetimeJob     Lifetime = "job"
	LifetimeService Lifetime = "service"
)

// TaskInfo is a snapshot of task information
// It contains both common fields and type-specific fields
type AttemptInfo struct {
	Attempt            int
	Status             TaskStatus
	StartTime, EndTime time.Time
	Error, Output      string
}

type TaskInfo struct {
	Attempt         int
	Attempts        []AttemptInfo
	ParentSessionID string
	OwnerAgentID    string // Empty identifies the main agent.

	// Common fields
	ID          string
	Type        TaskType
	Description string
	Status      TaskStatus
	StartTime   time.Time
	EndTime     time.Time
	Output      string
	OutputFile  string
	OutputError string // First output-log failure, separate from execution outcome.
	// OutputTruncated reports that Output is a bounded tail rather than
	// everything retained; only a Preview read sets it.
	OutputTruncated bool
	Progress        string // Latest meaningful activity, separate from final model-facing output.
	Error           string
	Failure         FailureInfo
	Lifetime        Lifetime

	// Bash-specific fields
	Command  string
	PID      int
	ExitCode int

	// Agent-specific fields
	AgentName      string
	PermissionMode string // Effective launch-time mode for ordinary subagent tasks.
	AgentSessionID string
	Model          string // Model the launch resolved to, as its lease reported it.
	StepCount      int
	TokenUsage     int
	LiveView       string // Current display for a running structured task, if any.
}
