// Package task defines the task model and the Task Analyzer that produces a
// structured TaskProfile. The analyzer is pure: it never calls a model, so
// trivial tasks never trigger expensive reasoning.
package task

import (
	"time"

	"omniharness/internal/budget"
)

// Status of a task or agent.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusWaiting   Status = "waiting_approval"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Complexity of a task.
type Complexity string

const (
	ComplexityLow    Complexity = "LOW"
	ComplexityMedium Complexity = "MEDIUM"
	ComplexityHigh   Complexity = "HIGH"
)

// Domain of a task.
type Domain string

const (
	DomainSoftware Domain = "SOFTWARE"
	DomainResearch Domain = "RESEARCH"
	DomainData     Domain = "DATA"
	DomainOps      Domain = "OPS"
	DomainWriting  Domain = "WRITING"
	DomainGeneral  Domain = "GENERAL"
)

// Level is a three-point scale used for ambiguity, risk and context size.
type Level string

const (
	LevelLow    Level = "LOW"
	LevelMedium Level = "MEDIUM"
	LevelHigh   Level = "HIGH"
	LevelLarge  Level = "LARGE"
	LevelSmall  Level = "SMALL"
)

// Verification describes how strictly results must be verified.
type Verification string

const (
	VerificationNone        Verification = "NONE"
	VerificationRecommended Verification = "RECOMMENDED"
	VerificationRequired    Verification = "REQUIRED"
)

// Spec is the immutable description of what the user asked for.
type Spec struct {
	Prompt    string            `json:"prompt"`
	CWD       string            `json:"cwd,omitempty"`
	SessionID string            `json:"sessionId,omitempty"`
	Budget    budget.Budget     `json:"budget,omitempty"`
	Deadline  time.Time         `json:"deadline,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// Profile is the structured result of task analysis. Every judgment carries an
// explanation in Signals so decisions are auditable.
type Profile struct {
	Domain         Domain     `json:"domain"`
	Complexity     Complexity `json:"complexity"`
	Ambiguity      Level      `json:"ambiguity"`
	Risk           Level      `json:"risk"`
	Context        Level      `json:"context"`
	Tools          []string   `json:"tools,omitempty"`
	Parallelizable bool       `json:"parallelizable"`
	// ModifiesFiles reports whether the request asks for a change on disk.
	// Only such tasks are held to a "the working tree actually changed"
	// check; asking a question about the code is not a failed edit.
	ModifiesFiles       bool         `json:"modifiesFiles"`
	Verification        Verification `json:"verification"`
	EstimatedCostUSD    float64      `json:"estimatedCostUsd"`
	EstimatedLatencyMS  int64        `json:"estimatedLatencyMs"`
	ApprovalRecommended bool         `json:"approvalRecommended"`
	Confidence          float64      `json:"confidence"`
	Signals             []string     `json:"signals,omitempty"`
	// AcceptanceCriteria are concrete, checkable statements of what "done"
	// looks like for this specific task — grounded in the actual request,
	// not a generic checklist. Set only by DeepAnalyzer's optional
	// model-based pass; Analyzer.Analyze alone never populates it, so nil
	// here means either the pass was skipped (see Profile.worthDeepening)
	// or it ran and found nothing usable to add. Nothing may assume it is
	// present.
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
}

// Result is the outcome of a completed task.
type Result struct {
	Summary string `json:"summary,omitempty"`
	Output  string `json:"output,omitempty"`
	// Artifacts lists files or resources produced by the task.
	Artifacts []string `json:"artifacts,omitempty"`
	// Evidence lists verification evidence (test names, build outputs...).
	Evidence []string `json:"evidence,omitempty"`
}

// Task is the durable runtime representation of a task.
type Task struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Spec      Spec      `json:"spec"`
	Profile   Profile   `json:"profile,omitempty"`
	Strategy  string    `json:"strategy,omitempty"`
	Status    Status    `json:"status"`
	Repairs   int       `json:"repairs"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Result    *Result   `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
}
