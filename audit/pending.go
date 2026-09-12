package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/trnahnh/kiln/tracing"
)

const timestampLayout = "2006-01-02T15:04:05.000000Z"

// Pending is an event committed in a CR's status, in the same write as the transition it
// records, until the broker has acknowledged it (ADR-0022). Details is the event's details
// as JSON text and Headers the W3C trace headers captured at the transition, so a record
// produced after a restart still joins the request's trace.
type Pending struct {
	EventID   string            `json:"eventId"`
	Actor     string            `json:"actor"`
	Action    string            `json:"action"`
	Resource  string            `json:"resource"`
	Timestamp string            `json:"timestamp"`
	Details   string            `json:"details"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// NewPending captures e and the span in ctx for later delivery.
func NewPending(ctx context.Context, e Event) (Pending, error) {
	if err := e.Validate(); err != nil {
		return Pending{}, err
	}
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return Pending{}, fmt.Errorf("details of %s: %w", e.EventID, err)
	}
	return Pending{
		EventID:   e.EventID,
		Actor:     e.Actor,
		Action:    e.Action,
		Resource:  e.Resource,
		Timestamp: e.Timestamp.UTC().Truncate(time.Microsecond).Format(timestampLayout),
		Details:   string(raw),
		Headers:   tracing.Headers(ctx),
	}, nil
}

// Event is the wire event p was made from.
func (p Pending) Event() (Event, error) {
	at, err := time.Parse(timestampLayout, p.Timestamp)
	if err != nil {
		return Event{}, fmt.Errorf("timestamp of %s: %w", p.EventID, err)
	}
	details := map[string]any{}
	if p.Details != "" {
		if err := json.Unmarshal([]byte(p.Details), &details); err != nil {
			return Event{}, fmt.Errorf("details of %s: %w", p.EventID, err)
		}
	}
	return Event{EventID: p.EventID, Actor: p.Actor, Action: p.Action, Resource: p.Resource, Timestamp: at, Details: details}, nil
}

func (in *Pending) DeepCopyInto(out *Pending) {
	*out = *in
	if in.Headers != nil {
		out.Headers = maps.Clone(in.Headers)
	}
}

func (in *Pending) DeepCopy() *Pending {
	if in == nil {
		return nil
	}
	out := new(Pending)
	in.DeepCopyInto(out)
	return out
}
