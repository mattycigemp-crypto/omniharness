package event

import (
	"time"

	"omniharness/internal/task"
)

// This file defines the typed payloads for every event the runtime can
// publish. Payload structs live here so the event package is the single
// vocabulary of the system; fields are JSON-friendly and self-contained.

// Register all payload factories so sessions can be replayed into typed
// structs.
func init() {
	register := func(f func() Payload) { Register(f) }
	register(func() Payload { return &SessionStartedData{} })
	register(func() Payload { return &SessionEndedData{} })
	register(func() Payload { return &TaskCreatedData{} })
	register(func() Payload { return &TaskAnalyzedData{} })
	register(func() Payload { return &TaskStateData{} })
	register(func() Payload { return &TaskPausedData{} })
	register(func() Payload { return &TaskResumedData{} })
	register(func() Payload { return &TaskCancelledData{} })
	register(func() Payload { return &TaskCompletedData{} })
	register(func() Payload { return &TaskFailedData{} })
	register(func() Payload { return &StrategySelectedData{} })
	register(func() Payload { return &AgentCreatedData{} })
	register(func() Payload { return &AgentStateData{} })
	register(func() Payload { return &AgentStartedData{} })
	register(func() Payload { return &AgentPausedData{} })
	register(func() Payload { return &AgentResumedData{} })
	register(func() Payload { return &AgentCompletedData{} })
	register(func() Payload { return &AgentFailedData{} })
	register(func() Payload { return &AgentCancelledData{} })
	register(func() Payload { return &AgentTranscriptData{} })
	register(func() Payload { return &ModelRequestedData{} })
	register(func() Payload { return &ModelRespondedData{} })
	register(func() Payload { return &ModelFailedData{} })
	register(func() Payload { return &ToolRequestedData{} })
	register(func() Payload { return &ToolStartedData{} })
	register(func() Payload { return &ToolFinishedData{} })
	register(func() Payload { return &ToolFailedData{} })
	register(func() Payload { return &ObservationCreatedData{} })
	register(func() Payload { return &ContextData{} })
	register(func() Payload { return &ContextCondensedData{} })
	register(func() Payload { return &EvaluationData{} })
	register(func() Payload { return &EvaluationCompletedData{} })
	register(func() Payload { return &RepairData{} })
	register(func() Payload { return &RepairCompletedData{} })
	register(func() Payload { return &ApprovalData{} })
	register(func() Payload { return &ApprovalGrantedData{} })
	register(func() Payload { return &ApprovalDeniedData{} })
	register(func() Payload { return &BudgetExceededData{} })
	register(func() Payload { return &CheckpointSavedData{} })
	register(func() Payload { return &LogMessageData{} })
}

// SessionStartedData is published when a session begins.
type SessionStartedData struct {
	CWD   string `json:"cwd"`
	Title string `json:"title,omitempty"`
}

func (*SessionStartedData) EventType() Type { return SessionStarted }

// SessionEndedData is published when a session terminates.
type SessionEndedData struct {
	Reason string `json:"reason,omitempty"`
}

func (*SessionEndedData) EventType() Type { return SessionEnded }

// TaskCreatedData accompanies TaskCreated.
type TaskCreatedData struct {
	Prompt string `json:"prompt"`
}

func (*TaskCreatedData) EventType() Type { return TaskCreated }

// TaskAnalyzedData accompanies TaskAnalyzed and carries the full profile.
type TaskAnalyzedData struct {
	Profile task.Profile `json:"profile"`
}

func (*TaskAnalyzedData) EventType() Type { return TaskAnalyzed }

// TaskStateData accompanies lifecycle transitions (started/paused/resumed/
// cancelled) and status changes.
type TaskStateData struct {
	Status  task.Status `json:"status"`
	Message string      `json:"message,omitempty"`
}

func (*TaskStateData) EventType() Type { return TaskStarted } // overridden below

// ---- task state events reuse TaskStateData with distinct types ----

// TaskPausedData accompanies TaskPaused.
type TaskPausedData TaskStateData

func (*TaskPausedData) EventType() Type { return TaskPaused }

// TaskResumedData accompanies TaskResumed.
type TaskResumedData TaskStateData

func (*TaskResumedData) EventType() Type { return TaskResumed }

// TaskCancelledData accompanies TaskCancelled.
type TaskCancelledData TaskStateData

func (*TaskCancelledData) EventType() Type { return TaskCancelled }

// TaskCompletedData accompanies TaskCompleted.
type TaskCompletedData struct {
	Summary   string   `json:"summary,omitempty"`
	Output    string   `json:"output,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

func (*TaskCompletedData) EventType() Type { return TaskCompleted }

// TaskFailedData accompanies TaskFailed.
type TaskFailedData struct {
	Status task.Status `json:"status"`
	Error  string      `json:"error,omitempty"`
}

func (*TaskFailedData) EventType() Type { return TaskFailed }

// StrategySelectedData accompanies StrategySelected.
type StrategySelectedData struct {
	Strategy string   `json:"strategy"`
	Reason   string   `json:"reason"`
	Steps    []string `json:"steps,omitempty"`
}

func (*StrategySelectedData) EventType() Type { return StrategySelected }

// AgentCreatedData accompanies AgentCreated.
type AgentCreatedData struct {
	Role      string `json:"role"`
	Model     string `json:"model"`
	TaskID    string `json:"taskId"`
	SessionID string `json:"sessionId"`
}

func (*AgentCreatedData) EventType() Type { return AgentCreated }

// AgentStateData accompanies agent lifecycle transitions.
type AgentStateData struct {
	Role    string        `json:"role,omitempty"`
	Status  task.Status   `json:"status"`
	Model   string        `json:"model,omitempty"`
	Action  string        `json:"action,omitempty"`
	Tokens  int64         `json:"tokens,omitempty"`
	CostUSD float64       `json:"costUsd,omitempty"`
	Latency time.Duration `json:"latency,omitempty"`
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
}

func (*AgentStateData) EventType() Type { return AgentUpdated }

// AgentStartedData accompanies AgentStarted.
type AgentStartedData AgentStateData

func (*AgentStartedData) EventType() Type { return AgentStarted }

// AgentPausedData accompanies AgentPaused.
type AgentPausedData AgentStateData

func (*AgentPausedData) EventType() Type { return AgentPaused }

// AgentResumedData accompanies AgentResumed.
type AgentResumedData AgentStateData

func (*AgentResumedData) EventType() Type { return AgentResumed }

// AgentCompletedData accompanies AgentCompleted.
type AgentCompletedData AgentStateData

func (*AgentCompletedData) EventType() Type { return AgentCompleted }

// AgentFailedData accompanies AgentFailed.
type AgentFailedData AgentStateData

func (*AgentFailedData) EventType() Type { return AgentFailed }

// AgentCancelledData accompanies AgentCancelled.
type AgentCancelledData AgentStateData

func (*AgentCancelledData) EventType() Type { return AgentCancelled }

// AgentTranscriptData carries a transcript update for an agent (used for
// durable resumability and session replay).
type AgentTranscriptData struct {
	Messages int `json:"messages"`
}

func (*AgentTranscriptData) EventType() Type { return AgentTranscript }

// ModelRequestedData accompanies ModelRequested.
type ModelRequestedData struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	TaskID   string `json:"taskId,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
	Stream   bool   `json:"stream"`
	// Reason explains the selection (config capability, explicit ref, or
	// performance-memory override).
	Reason string `json:"reason,omitempty"`
}

func (*ModelRequestedData) EventType() Type { return ModelRequested }

// ModelRespondedData accompanies ModelResponded.
type ModelRespondedData struct {
	Provider  string        `json:"provider,omitempty"`
	Model     string        `json:"model"`
	TaskID    string        `json:"taskId,omitempty"`
	AgentID   string        `json:"agentId,omitempty"`
	TokensIn  int64         `json:"tokensIn"`
	TokensOut int64         `json:"tokensOut"`
	CostUSD   float64       `json:"costUsd"`
	Latency   time.Duration `json:"latency"`
}

func (*ModelRespondedData) EventType() Type { return ModelResponded }

// ModelFailedData accompanies ModelFailed.
type ModelFailedData struct {
	Model    string `json:"model"`
	TaskID   string `json:"taskId,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

func (*ModelFailedData) EventType() Type { return ModelFailed }

// ToolRequestedData accompanies ToolRequested (before policy evaluation).
type ToolRequestedData struct {
	Tool    string `json:"tool"`
	Input   string `json:"input,omitempty"` // truncated for logs
	Risk    string `json:"risk"`
	AgentID string `json:"agentId,omitempty"`
}

func (*ToolRequestedData) EventType() Type { return ToolRequested }

// ToolStartedData accompanies ToolStarted.
type ToolStartedData struct {
	Tool    string `json:"tool"`
	AgentID string `json:"agentId,omitempty"`
}

func (*ToolStartedData) EventType() Type { return ToolStarted }

// ToolFinishedData accompanies ToolCompleted and ToolFailed.
type ToolFinishedData struct {
	Tool      string        `json:"tool"`
	AgentID   string        `json:"agentId,omitempty"`
	Status    string        `json:"status"` // "completed" | "failed" | "denied" | "cancelled"
	Duration  time.Duration `json:"duration"`
	Error     string        `json:"error,omitempty"`
	OutputLen int           `json:"outputLen"`
}

func (*ToolFinishedData) EventType() Type { return ToolCompleted }

// ToolFailedData accompanies ToolFailed.
type ToolFailedData ToolFinishedData

func (*ToolFailedData) EventType() Type { return ToolFailed }

// ObservationCreatedData accompanies ObservationCreated (tool result fed
// back into the agent loop).
type ObservationCreatedData struct {
	Tool      string `json:"tool"`
	AgentID   string `json:"agentId,omitempty"`
	Summary   string `json:"summary"`
	OutputLen int    `json:"outputLen"`
}

func (*ObservationCreatedData) EventType() Type { return ObservationCreated }

// ContextData accompanies ContextUpdated and ContextCondensed.
type ContextData struct {
	TokensBefore int64  `json:"tokensBefore"`
	TokensAfter  int64  `json:"tokensAfter"`
	Reason       string `json:"reason,omitempty"`
}

func (*ContextData) EventType() Type { return ContextUpdated }

// ContextCondensedData accompanies ContextCondensed.
type ContextCondensedData ContextData

func (*ContextCondensedData) EventType() Type { return ContextCondensed }

// EvaluationData accompanies EvaluationStarted and EvaluationCompleted.
type EvaluationData struct {
	Evaluator string `json:"evaluator"`
	Outcome   string `json:"outcome"` // PASS | FAIL | PASS_WITH_WARNINGS | NEEDS_REVIEW
	Detail    string `json:"detail,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
}

func (*EvaluationData) EventType() Type { return EvaluationStarted }

// EvaluationCompletedData accompanies EvaluationCompleted.
type EvaluationCompletedData EvaluationData

func (*EvaluationCompletedData) EventType() Type { return EvaluationComplete }

// RepairData accompanies RepairStarted and RepairCompleted.
type RepairData struct {
	Attempt  int      `json:"attempt"`
	Strategy string   `json:"strategy"`
	Reason   string   `json:"reason"`
	Changed  []string `json:"changed,omitempty"` // variables altered
}

func (*RepairData) EventType() Type { return RepairStarted }

// RepairCompletedData accompanies RepairCompleted.
type RepairCompletedData RepairData

func (*RepairCompletedData) EventType() Type { return RepairCompleted }

// ApprovalData accompanies ApprovalRequested/Granted/Denied.
type ApprovalData struct {
	Tool      string `json:"tool,omitempty"`
	Action    string `json:"action,omitempty"`
	Risk      string `json:"risk,omitempty"`
	Requester string `json:"requester,omitempty"` // agent or "user"
	Reason    string `json:"reason,omitempty"`
	Decision  string `json:"decision,omitempty"` // granted | denied
}

func (*ApprovalData) EventType() Type { return ApprovalRequested }

// ApprovalGrantedData accompanies ApprovalGranted.
type ApprovalGrantedData ApprovalData

func (*ApprovalGrantedData) EventType() Type { return ApprovalGranted }

// ApprovalDeniedData accompanies ApprovalDenied.
type ApprovalDeniedData ApprovalData

func (*ApprovalDeniedData) EventType() Type { return ApprovalDenied }

// BudgetExceededData accompanies BudgetExceeded.
type BudgetExceededData struct {
	Dimension string `json:"dimension"`
	TaskID    string `json:"taskId,omitempty"`
}

func (*BudgetExceededData) EventType() Type { return BudgetExceeded }

// CheckpointSavedData accompanies CheckpointSaved.
type CheckpointSavedData struct {
	TaskID  string `json:"taskId,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func (*CheckpointSavedData) EventType() Type { return CheckpointSaved }

// LogMessageData accompanies LogMessage — free-form structured log lines
// that are also observable as events.
type LogMessageData struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (*LogMessageData) EventType() Type { return LogMessage }
