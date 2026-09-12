package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Store is where a controller's pending events live: its CRs' status (ADR-0022).
type Store interface {
	// Pending returns the events waiting on the object named key, oldest first.
	Pending(ctx context.Context, key string) ([]Pending, error)
	// Delivered removes p from the object named key without disturbing events appended
	// since; a p that is already gone is not an error.
	Delivered(ctx context.Context, key string, p Pending) error
}

// Signaler is what a reconciler holds: it asks for an object's pending events to be
// delivered once the status patch that committed them has succeeded.
type Signaler interface {
	Signal(key string)
}

type DrainerOptions struct {
	Deliverer Deliverer
	Store     Store
	// Threshold is the pending count on one object at which OnBacklog is called.
	Threshold int
	OnBacklog func(key string, pending int)
	// Timeout bounds one delivery attempt.
	Timeout time.Duration
	// Backoff is the wait after a failed attempt, doubled up to MaxBackoff.
	Backoff, MaxBackoff time.Duration
	// Registerer receives the pending gauge; nil leaves it unregistered.
	Registerer prometheus.Registerer
	// Sleep is replaced in tests.
	Sleep func(context.Context, time.Duration)
}

// Drainer delivers the pending events of the objects it is signalled about, in the order
// they were committed, and removes each one once the broker has acknowledged it. A
// reconcile only signals; it never waits here.
type Drainer struct {
	deliver   Deliverer
	store     Store
	threshold int
	onBacklog func(string, int)
	timeout   time.Duration
	backoff   time.Duration
	maxBack   time.Duration
	sleep     func(context.Context, time.Duration)
	gauge     prometheus.Gauge

	mu      sync.Mutex
	waiting map[string]struct{}
	counts  map[string]int
	wake    chan struct{}
}

func NewDrainer(o DrainerOptions) (*Drainer, error) {
	if o.Deliverer == nil || o.Store == nil {
		return nil, errors.New("outbox needs a deliverer and a store")
	}
	if o.Threshold <= 0 {
		o.Threshold = 100
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	d := &Drainer{
		deliver:   o.Deliverer,
		store:     o.Store,
		threshold: o.Threshold,
		onBacklog: o.OnBacklog,
		timeout:   o.Timeout,
		backoff:   o.Backoff,
		maxBack:   o.MaxBackoff,
		sleep:     o.Sleep,
		gauge:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "kiln_audit_outbox_pending", Help: "Audit events committed in CR status and not yet acknowledged by Kafka (ADR-0022)."}),
		waiting:   map[string]struct{}{},
		counts:    map[string]int{},
		wake:      make(chan struct{}, 1),
	}
	if o.Registerer != nil {
		if err := o.Registerer.Register(d.gauge); err != nil {
			var already prometheus.AlreadyRegisteredError
			if !errors.As(err, &already) {
				return nil, fmt.Errorf("register metrics: %w", err)
			}
			d.gauge = already.ExistingCollector.(prometheus.Gauge)
		}
	}
	return d, nil
}

// Signal asks for the object named key to be drained; it never blocks.
func (d *Drainer) Signal(key string) {
	d.mu.Lock()
	d.waiting[key] = struct{}{}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Start drains until ctx ends. It satisfies controller-runtime's Runnable.
func (d *Drainer) Start(ctx context.Context) error {
	backoff := d.backoff
	for {
		key, ok := d.next()
		if !ok {
			select {
			case <-ctx.Done():
				return nil
			case <-d.wake:
				continue
			}
		}
		if err := d.drain(ctx, key); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.Signal(key)
			d.sleep(ctx, backoff)
			backoff = min(backoff*2, d.maxBack)
			continue
		}
		backoff = d.backoff
	}
}

func (d *Drainer) next() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.waiting) == 0 {
		return "", false
	}
	keys := make([]string, 0, len(d.waiting))
	for k := range d.waiting {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	delete(d.waiting, keys[0])
	return keys[0], true
}

func (d *Drainer) drain(ctx context.Context, key string) error {
	pending, err := d.store.Pending(ctx, key)
	if err != nil {
		return err
	}
	d.observe(key, len(pending))
	if len(pending) >= d.threshold && d.onBacklog != nil {
		d.onBacklog(key, len(pending))
	}
	for i, p := range pending {
		e, err := p.Event()
		if err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, d.timeout)
		err = d.deliver.Deliver(attempt, e, p.Headers)
		cancel()
		if err != nil {
			return fmt.Errorf("deliver %s: %w", p.EventID, err)
		}
		if err := d.store.Delivered(ctx, key, p); err != nil {
			return err
		}
		d.observe(key, len(pending)-i-1)
	}
	return nil
}

func (d *Drainer) observe(key string, pending int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if pending == 0 {
		delete(d.counts, key)
	} else {
		d.counts[key] = pending
	}
	total := 0
	for _, n := range d.counts {
		total += n
	}
	d.gauge.Set(float64(total))
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
