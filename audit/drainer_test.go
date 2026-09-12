package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/trnahnh/kiln/tracing"
)

// memoryStore is a CR status in memory: one pending list per key.
type memoryStore struct {
	mu      sync.Mutex
	pending map[string][]Pending
	// appendDuringDelivered simulates a reconcile committing a new event while the
	// drainer is clearing an old one.
	appendDuringDelivered *Pending
}

func (s *memoryStore) Pending(_ context.Context, key string) ([]Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Pending(nil), s.pending[key]...), nil
}

func (s *memoryStore) Delivered(_ context.Context, key string, p Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.pending[key][:0:0]
	for _, q := range s.pending[key] {
		if q.EventID != p.EventID {
			kept = append(kept, q)
		}
	}
	if s.appendDuringDelivered != nil {
		kept = append(kept, *s.appendDuringDelivered)
		s.appendDuringDelivered = nil
	}
	s.pending[key] = kept
	return nil
}

func (s *memoryStore) commit(key string, p Pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[key] = append(s.pending[key], p)
}

func (s *memoryStore) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending[key])
}

// flakyDeliverer fails the first n attempts, then records like a Recorder.
type flakyDeliverer struct {
	Recorder
	mu       sync.Mutex
	failures int
	attempts int
}

func (f *flakyDeliverer) Deliver(ctx context.Context, e Event, headers map[string]string) error {
	f.mu.Lock()
	f.attempts++
	fail := f.attempts <= f.failures
	f.mu.Unlock()
	if fail {
		return errors.New("broker unreachable")
	}
	return f.Recorder.Deliver(ctx, e, headers)
}

func pending(t *testing.T, ctx context.Context, id string) Pending {
	t.Helper()
	p, err := NewPending(ctx, sample(DeterministicID(id)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runDrainer(t *testing.T, d *Drainer) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Start(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDrainerDeliversInOrderAndClears(t *testing.T) {
	store := &memoryStore{pending: map[string][]Pending{}}
	rec := &Recorder{}
	reg := prometheus.NewRegistry()
	d, err := NewDrainer(DrainerOptions{Deliverer: rec, Store: store, Registerer: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer runDrainer(t, d)()

	for _, id := range []string{"a", "b", "c"} {
		store.commit("ns/db", pending(t, context.Background(), id))
	}
	d.Signal("ns/db")

	eventually(t, "three deliveries", func() bool { return len(rec.Events()) == 3 })
	got := rec.Events()
	for i, id := range []string{"a", "b", "c"} {
		if got[i].EventID != DeterministicID(id) {
			t.Errorf("event %d: got %s, want %s", i, got[i].EventID, DeterministicID(id))
		}
	}
	eventually(t, "status cleared", func() bool { return store.count("ns/db") == 0 })
	if v := testutil.ToFloat64(d.gauge); v != 0 {
		t.Errorf("pending gauge after drain = %v, want 0", v)
	}
}

func TestDrainerRetriesUntilTheBrokerAcknowledges(t *testing.T) {
	store := &memoryStore{pending: map[string][]Pending{}}
	del := &flakyDeliverer{failures: 3}
	var slept []time.Duration
	var mu sync.Mutex
	d, err := NewDrainer(DrainerOptions{
		Deliverer: del, Store: store,
		Backoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond,
		Sleep: func(_ context.Context, w time.Duration) {
			mu.Lock()
			slept = append(slept, w)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runDrainer(t, d)()

	store.commit("ns/db", pending(t, context.Background(), "retry"))
	d.Signal("ns/db")

	eventually(t, "delivery after failures", func() bool { return len(del.Events()) == 1 })
	eventually(t, "status cleared", func() bool { return store.count("ns/db") == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 3 || slept[0] != time.Millisecond || slept[1] != 2*time.Millisecond || slept[2] != 4*time.Millisecond {
		t.Errorf("backoff must double up to the cap, slept %v", slept)
	}
}

func TestDrainerKeepsAnEventCommittedWhileClearingAnother(t *testing.T) {
	store := &memoryStore{pending: map[string][]Pending{}}
	rec := &Recorder{}
	d, err := NewDrainer(DrainerOptions{Deliverer: rec, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer runDrainer(t, d)()

	late := pending(t, context.Background(), "late")
	store.commit("ns/db", pending(t, context.Background(), "early"))
	store.appendDuringDelivered = &late
	d.Signal("ns/db")

	eventually(t, "the early event", func() bool { return len(rec.Events()) >= 1 })
	d.Signal("ns/db")
	eventually(t, "the late event too", func() bool { return len(rec.Events()) == 2 })
	if got := rec.Events()[1].EventID; got != late.EventID {
		t.Errorf("second delivery = %s, want the event committed mid-drain", got)
	}
	eventually(t, "status cleared", func() bool { return store.count("ns/db") == 0 })
}

func TestDrainerCarriesTheTransitionsTrace(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	defer otel.SetTracerProvider(noop.NewTracerProvider())
	ctx, span := otel.Tracer("test").Start(context.Background(), "PROVISION")
	p := pending(t, ctx, "traced")
	span.End()

	store := &memoryStore{pending: map[string][]Pending{"ns/db": {p}}}
	rec := &Recorder{}
	d, err := NewDrainer(DrainerOptions{Deliverer: rec, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer runDrainer(t, d)()
	d.Signal("ns/db")

	eventually(t, "delivery", func() bool { return len(rec.Events()) == 1 })
	if got := rec.TraceIDs()[0]; got != tracing.TraceID(ctx) {
		t.Errorf("delivered under trace %q, want the one captured at the transition %q (ADR-0021)", got, tracing.TraceID(ctx))
	}
}

func TestDrainerReportsABacklog(t *testing.T) {
	store := &memoryStore{pending: map[string][]Pending{}}
	del := &flakyDeliverer{failures: 1 << 30}
	var reported int
	d, err := NewDrainer(DrainerOptions{
		Deliverer: del, Store: store, Threshold: 3,
		OnBacklog: func(_ string, n int) { reported = n },
		Sleep:     func(context.Context, time.Duration) {},
		Backoff:   time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		store.commit("ns/db", pending(t, context.Background(), id))
	}
	if err := d.drain(t.Context(), "ns/db"); err == nil {
		t.Fatal("drain must report the broker failure")
	}
	if reported != 3 {
		t.Errorf("backlog reported %d, want 3", reported)
	}
	if v := testutil.ToFloat64(d.gauge); v != 3 {
		t.Errorf("pending gauge = %v, want 3", v)
	}
	if store.count("ns/db") != 3 {
		t.Error("nothing may be cleared before the broker acknowledges")
	}
}

func TestPendingRoundTripsTheEvent(t *testing.T) {
	e := sample(DeterministicID("round"))
	p, err := NewPending(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	back, err := p.Event()
	if err != nil {
		t.Fatal(err)
	}
	want := e.Timestamp.Truncate(time.Microsecond)
	if back.EventID != e.EventID || back.Actor != e.Actor || back.Action != e.Action || back.Resource != e.Resource ||
		!back.Timestamp.Equal(want) || back.Details["outcome"] != "Ready" {
		t.Errorf("round trip changed the event: %+v", back)
	}
	if _, err := NewPending(context.Background(), Event{}); err == nil {
		t.Error("an invalid event must not be committed")
	}
}
