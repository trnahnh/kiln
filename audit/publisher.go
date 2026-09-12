package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/trnahnh/kiln/tracing"
)

// Publisher accepts an event and returns at once; delivery is the implementation's
// business (ADR-0017). ctx carries the span the event belongs to, which travels with the
// record as W3C headers (ADR-0021).
type Publisher interface {
	Publish(context.Context, Event)
}

// Deliverer publishes one event and returns once the broker has acknowledged it, or with
// the reason it could not; headers are the trace headers captured when the event was
// committed (ADR-0022).
type Deliverer interface {
	Deliver(ctx context.Context, e Event, headers map[string]string) error
}

// Discard is the publisher of a controller with no brokers configured.
type Discard struct{}

func (Discard) Publish(context.Context, Event) {}

func (Discard) Deliver(context.Context, Event, map[string]string) error { return nil }

// Recorder keeps every event in memory with the trace it was published under; tests read
// them back.
type Recorder struct {
	mu     sync.Mutex
	events []Event
	traces []string
}

func (r *Recorder) Publish(ctx context.Context, e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	r.traces = append(r.traces, tracing.TraceID(ctx))
}

func (r *Recorder) Deliver(ctx context.Context, e Event, headers map[string]string) error {
	r.Publish(tracing.FromHeaders(ctx, headers), e)
	return nil
}

func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// TraceIDs are the traces the recorded events were published under, in order; an empty
// string marks an event published outside any span.
func (r *Recorder) TraceIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.traces...)
}

// Options configure a Kafka publisher. Brokers is the only required field.
type Options struct {
	Brokers []string
	Topic   string
	// Buffer is how many events may wait for the producer before new ones are dropped.
	Buffer int
	// DeliveryTimeout bounds the retries for one event before it is given up.
	DeliveryTimeout time.Duration
	// OnFailure is called for every event that is dropped or given up, with the reason.
	OnFailure func(Event, error)
	// Registerer receives the publish counters; nil leaves them unregistered.
	Registerer prometheus.Registerer
}

type Kafka struct {
	client    *kgo.Client
	topic     string
	queue     chan queued
	onFailure func(Event, error)
	published *prometheus.CounterVec
	failures  *prometheus.CounterVec
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// queued is an event with the propagation headers captured when it was published, so the
// drain goroutine needs no context of its own.
type queued struct {
	event   Event
	headers map[string]string
}

var ErrBufferFull = errors.New("audit publish buffer is full")

func NewKafka(o Options) (*Kafka, error) {
	if len(o.Brokers) == 0 {
		return nil, errors.New("no brokers")
	}
	if o.Topic == "" {
		o.Topic = Topic
	}
	if o.Buffer <= 0 {
		o.Buffer = 1024
	}
	if o.DeliveryTimeout <= 0 {
		o.DeliveryTimeout = 2 * time.Minute
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(o.Brokers...),
		kgo.DefaultProduceTopic(o.Topic),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordDeliveryTimeout(o.DeliveryTimeout),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka client: %w", err)
	}
	k := &Kafka{
		client:    client,
		topic:     o.Topic,
		queue:     make(chan queued, o.Buffer),
		onFailure: o.OnFailure,
		published: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kiln_audit_events_published_total", Help: "Audit events acknowledged by Kafka."}, []string{"action"}),
		failures:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kiln_audit_publish_failures_total", Help: "Audit events dropped or given up (ADR-0017)."}, []string{"action"}),
	}
	if o.Registerer != nil {
		for _, c := range []prometheus.Collector{k.published, k.failures} {
			if err := o.Registerer.Register(c); err != nil {
				var already prometheus.AlreadyRegisteredError
				if !errors.As(err, &already) {
					return nil, fmt.Errorf("register metrics: %w", err)
				}
			}
		}
	}
	k.wg.Add(1)
	go k.drain()
	return k, nil
}

// Publish never blocks: a full buffer drops the event and reports it.
func (k *Kafka) Publish(ctx context.Context, e Event) {
	if err := e.Validate(); err != nil {
		k.fail(e, err)
		return
	}
	select {
	case k.queue <- queued{event: e, headers: tracing.Headers(ctx)}:
	default:
		k.fail(e, ErrBufferFull)
	}
}

// Deliver produces e and waits for the acknowledgement. A validation failure is counted
// and reported like a drop, since no retry can fix it; a broker failure is only returned.
func (k *Kafka) Deliver(ctx context.Context, e Event, headers map[string]string) error {
	if err := e.Validate(); err != nil {
		k.fail(e, err)
		return err
	}
	value, err := json.Marshal(e)
	if err != nil {
		k.fail(e, err)
		return err
	}
	rec := &kgo.Record{Topic: k.topic, Key: []byte(e.Resource), Value: value, Headers: recordHeaders(headers)}
	if err := k.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return err
	}
	k.published.WithLabelValues(e.Action).Inc()
	return nil
}

func (k *Kafka) drain() {
	defer k.wg.Done()
	for q := range k.queue {
		e := q.event
		value, err := json.Marshal(e)
		if err != nil {
			k.fail(e, err)
			continue
		}
		rec := &kgo.Record{Topic: k.topic, Key: []byte(e.Resource), Value: value, Headers: recordHeaders(q.headers)}
		k.client.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
			if err != nil {
				k.fail(e, err)
				return
			}
			k.published.WithLabelValues(e.Action).Inc()
		})
	}
}

func recordHeaders(headers map[string]string) []kgo.RecordHeader {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]kgo.RecordHeader, 0, len(keys))
	for _, key := range keys {
		out = append(out, kgo.RecordHeader{Key: key, Value: []byte(headers[key])})
	}
	return out
}

func (k *Kafka) fail(e Event, err error) {
	k.failures.WithLabelValues(e.Action).Inc()
	if k.onFailure != nil {
		k.onFailure(e, err)
	}
}

// Close stops accepting events, flushes what is buffered within ctx, and closes the client.
func (k *Kafka) Close(ctx context.Context) error {
	var err error
	k.closeOnce.Do(func() {
		close(k.queue)
		k.wg.Wait()
		err = k.client.Flush(ctx)
		k.client.Close()
	})
	return err
}
