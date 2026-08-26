// Package event is the internal spine of OmniHarness. All important state
// transitions are published as typed events; the TUI, telemetry, persistence
// and debugging are consumers. The package has zero internal dependencies so
// it can be imported from anywhere without creating cycles.
package event

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// Type identifies a kind of event. Values are stable strings suitable for
// persistence, filtering and replay.
type Type string

// All event types produced by the runtime.
const (
	TaskCreated        Type = "task.created"
	TaskAnalyzed       Type = "task.analyzed"
	TaskStarted        Type = "task.started"
	TaskPaused         Type = "task.paused"
	TaskResumed        Type = "task.resumed"
	TaskCompleted      Type = "task.completed"
	TaskFailed         Type = "task.failed"
	TaskCancelled      Type = "task.cancelled"
	StrategySelected   Type = "strategy.selected"
	AgentCreated       Type = "agent.created"
	AgentStarted       Type = "agent.started"
	AgentPaused        Type = "agent.paused"
	AgentResumed       Type = "agent.resumed"
	AgentUpdated       Type = "agent.updated"
	AgentCompleted     Type = "agent.completed"
	AgentFailed        Type = "agent.failed"
	AgentCancelled     Type = "agent.cancelled"
	AgentTranscript    Type = "agent.transcript"
	ModelRequested     Type = "model.requested"
	ModelResponded     Type = "model.responded"
	ModelFailed        Type = "model.failed"
	ToolRequested      Type = "tool.requested"
	ToolStarted        Type = "tool.started"
	ToolCompleted      Type = "tool.completed"
	ToolFailed         Type = "tool.failed"
	ObservationCreated Type = "observation.created"
	ContextUpdated     Type = "context.updated"
	ContextCondensed   Type = "context.condensed"
	EvaluationStarted  Type = "evaluation.started"
	EvaluationComplete Type = "evaluation.completed"
	RepairStarted      Type = "repair.started"
	RepairCompleted    Type = "repair.completed"
	ApprovalRequested  Type = "approval.requested"
	ApprovalGranted    Type = "approval.granted"
	ApprovalDenied     Type = "approval.denied"
	BudgetExceeded     Type = "budget.exceeded"
	SessionStarted     Type = "session.started"
	SessionEnded       Type = "session.ended"
	CheckpointSaved    Type = "checkpoint.saved"
	LogMessage         Type = "log.message"
)

// Payload is implemented by typed event payloads. Producers keep payload
// structs in their own packages and register factories for replay.
type Payload interface {
	EventType() Type
}

// Event is the envelope every event travels in.
type Event struct {
	ID        string          `json:"id"`
	Type      Type            `json:"type"`
	SessionID string          `json:"sessionId,omitempty"`
	TaskID    string          `json:"taskId,omitempty"`
	AgentID   string          `json:"agentId,omitempty"`
	Time      time.Time       `json:"time"`
	Data      json.RawMessage `json:"data,omitempty"`
	// Seq is the bus-wide publish order (stamped by Publish). It exists so
	// consumers can prove durability ordering ("every event published before
	// this point is durably handled"); it is never persisted.
	Seq uint64 `json:"-"`
}

// New builds an Event from a typed payload.
func New(p Payload) Event {
	data, err := json.Marshal(p)
	if err != nil {
		// Payloads are internal structs; marshaling them cannot fail in
		// practice. Fall back to an empty object rather than losing the event.
		data = []byte("{}")
	}
	return Event{
		ID:   newID(),
		Type: p.EventType(),
		Time: time.Now().UTC(),
		Data: data,
	}
}

// Decode reconstructs the typed payload of an event using the registered
// factory for its type.
func Decode(e Event) (Payload, error) {
	factory, ok := registry[e.Type]
	if !ok {
		return nil, &UnknownTypeError{Type: e.Type}
	}
	p := factory()
	if err := json.Unmarshal(e.Data, p); err != nil {
		return nil, err
	}
	return p, nil
}

// UnknownTypeError is returned when an event type has no registered factory.
type UnknownTypeError struct{ Type Type }

func (e *UnknownTypeError) Error() string {
	return "no payload registered for event type " + string(e.Type)
}

var registry = map[Type]func() Payload{}

// Register installs a factory that constructs an empty payload of a given
// event type. Packages call it from init().
func Register(factory func() Payload) {
	registry[factory().EventType()] = factory
}

// Bus fans events out to subscribers. It never blocks the publisher: each
// subscriber has a bounded buffer and the oldest events are dropped when the
// consumer cannot keep up.
type Bus struct {
	mu          sync.Mutex
	subscribers map[uint64]*subscriber
	nextID      uint64
	closed      bool
	seq         uint64 // publish order; monotonically increasing under mu
}

type subscriber struct {
	ch      chan Event
	filters map[Type]bool // nil means "all"
}

// NewBus creates an event bus.
func NewBus() *Bus {
	return &Bus{subscribers: make(map[uint64]*subscriber)}
}

// Publish delivers an event to every matching subscriber. It never blocks.
// The stamped Seq is the publish order, so a consumer that records the last
// seq it handled can prove "everything published before point T is handled"
// by comparing against Sequence() at T.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.seq++
	e.Seq = b.seq
	for _, s := range b.subscribers {
		if s.filters != nil && !s.filters[e.Type] {
			continue
		}
		select {
		case s.ch <- e:
		default:
			// Full: drop the oldest buffered event, then retry once.
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- e:
			default:
			}
		}
	}
	b.mu.Unlock()
}

// Sequence returns the highest publish sequence stamped so far. Events are
// delivered to each subscriber in publish order, so a consumer whose last
// handled seq equals Sequence() has seen every event published before the
// call.
func (b *Bus) Sequence() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Subscribe returns a channel of all events. The cancel function unsubscribes
// and closes the channel. buf is the buffer size before events are dropped.
func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	return b.subscribe(buf, nil)
}

// SubscribeTo is Subscribe restricted to the given event types.
func (b *Bus) SubscribeTo(buf int, types ...Type) (<-chan Event, func()) {
	filters := make(map[Type]bool, len(types))
	for _, t := range types {
		filters[t] = true
	}
	return b.subscribe(buf, filters)
}

func (b *Bus) subscribe(buf int, filters map[Type]bool) (<-chan Event, func()) {
	if buf < 1 {
		buf = 1
	}
	s := &subscriber{ch: make(chan Event, buf), filters: filters}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	id := b.nextID
	b.nextID++
	b.subscribers[id] = s
	b.mu.Unlock()
	return s.ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(s.ch)
		}
	}
}

// Close shuts the bus down and unsubscribes everyone.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subscribers {
		delete(b.subscribers, id)
		close(s.ch)
	}
}

// Snapshot returns the number of active subscribers (used in diagnostics).
func (b *Bus) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively impossible; fall back to a
		// timestamp-based id so the system keeps working.
		return time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return hex.EncodeToString(b[:])
}
