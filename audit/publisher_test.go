package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func sample(id string) Event {
	return Event{
		EventID:   id,
		Actor:     "user@company.com",
		Action:    ActionProvision,
		Resource:  ResourceRef("TenantDatabase", "team-checkout", "checkout-db"),
		Timestamp: time.Date(2026, 9, 4, 18, 0, 0, 123456789, time.UTC),
		Details:   map[string]any{"outcome": "Ready"},
	}
}

func TestWireShapeIsTheDocumentedOne(t *testing.T) {
	b, err := json.Marshal(sample(DeterministicID("a")))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"eventId", "actor", "action", "resource", "timestamp", "details"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	if got["timestamp"] != "2026-09-04T18:00:00.123456Z" {
		t.Errorf("timestamp must be UTC at microsecond precision, got %v", got["timestamp"])
	}
	if _, ok := got["prevHash"]; ok {
		t.Error("a wire event carries no chain fields (ADR-0018)")
	}
	b, _ = json.Marshal(Event{EventID: "x", Timestamp: time.Now()})
	if !strings.Contains(string(b), `"details":{}`) {
		t.Errorf("nil details must serialise as an empty object: %s", b)
	}
}

func TestDeterministicIDIsStableAndDistinct(t *testing.T) {
	a := DeterministicID("TenantDatabase/ns/x", ActionBackup, "20260904T180000Z")
	if a != DeterministicID("TenantDatabase/ns/x", ActionBackup, "20260904T180000Z") {
		t.Fatal("same parts must give the same id")
	}
	if a == DeterministicID("TenantDatabase/ns/x", ActionBackup, "20260904T180001Z") {
		t.Fatal("different transitions must give different ids")
	}
	if (Event{EventID: a, Actor: "a", Action: "b", Resource: "c", Timestamp: time.Now()}).Validate() != nil {
		t.Fatal("a deterministic id must be a valid uuid")
	}
}

func TestActorOf(t *testing.T) {
	if got := ActorOf(map[string]string{AnnotationRequestedBy: "dev@x"}, "tenantdatabase"); got != "dev@x" {
		t.Fatal(got)
	}
	if got := ActorOf(nil, "tenantdatabase"); got != "system:tenantdatabase" {
		t.Fatal(got)
	}
}

func TestKafkaDeliversToTheTopicKeyedByResource(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, Topic))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	pub, err := NewKafka(Options{Brokers: cluster.ListenAddrs()})
	if err != nil {
		t.Fatal(err)
	}
	e := sample(DeterministicID("deliver"))
	pub.Publish(e)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pub.Close(ctx); err != nil {
		t.Fatal(err)
	}

	consumer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics(Topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	fetches := consumer.PollFetches(ctx)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	recs := fetches.Records()
	if len(recs) != 1 {
		t.Fatalf("want one record, got %d", len(recs))
	}
	if string(recs[0].Key) != e.Resource {
		t.Errorf("record key must be the resource, got %q", recs[0].Key)
	}
	var got Event
	if err := json.Unmarshal(recs[0].Value, &got); err != nil {
		t.Fatal(err)
	}
	if got.EventID != e.EventID || got.Details["outcome"] != "Ready" {
		t.Errorf("round trip lost content: %+v", got)
	}
}

func TestKafkaNeverBlocksAndReportsDrops(t *testing.T) {
	var mu sync.Mutex
	var failed []error
	pub, err := NewKafka(Options{
		Brokers: []string{"127.0.0.1:1"},
		Buffer:  1,
		OnFailure: func(_ Event, err error) {
			mu.Lock()
			defer mu.Unlock()
			failed = append(failed, err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			pub.Publish(sample(DeterministicID("drop", string(rune('a'+i)))))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked with an unreachable broker (ADR-0017)")
	}
	pub.Publish(Event{EventID: "not-a-uuid"})
	mu.Lock()
	defer mu.Unlock()
	var full, invalid bool
	for _, err := range failed {
		if errors.Is(err, ErrBufferFull) {
			full = true
		} else {
			invalid = true
		}
	}
	if !full || !invalid {
		t.Fatalf("expected both buffer-full and validation failures to be reported, got %v", failed)
	}
}
