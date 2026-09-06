package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Publisher accepts an event and returns at once; delivery is the implementation's
// business (ADR-0017).
type Publisher interface {
	Publish(Event)
}

// Discard is the publisher of a controller with no brokers configured.
type Discard struct{}

func (Discard) Publish(Event) {}

// Recorder keeps every event in memory; tests read them back.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *Recorder) Publish(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
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
	queue     chan Event
	onFailure func(Event, error)
	published *prometheus.CounterVec
	failures  *prometheus.CounterVec
	wg        sync.WaitGroup
	closeOnce sync.Once
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
		queue:     make(chan Event, o.Buffer),
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
func (k *Kafka) Publish(e Event) {
	if err := e.Validate(); err != nil {
		k.fail(e, err)
		return
	}
	select {
	case k.queue <- e:
	default:
		k.fail(e, ErrBufferFull)
	}
}

func (k *Kafka) drain() {
	defer k.wg.Done()
	for e := range k.queue {
		value, err := json.Marshal(e)
		if err != nil {
			k.fail(e, err)
			continue
		}
		rec := &kgo.Record{Topic: k.topic, Key: []byte(e.Resource), Value: value}
		k.client.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
			if err != nil {
				k.fail(e, err)
				return
			}
			k.published.WithLabelValues(e.Action).Inc()
		})
	}
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
