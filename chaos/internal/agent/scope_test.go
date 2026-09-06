package agent

import (
	"reflect"
	"testing"
)

func TestAllowedFloorsAndNeverRoundsUp(t *testing.T) {
	cases := []struct {
		cap      int32
		matching int
		want     int
	}{
		{30, 2, 0},
		{30, 4, 1},
		{50, 4, 2},
		{100, 4, 4},
		{30, 10, 3},
		{30, 0, 0},
		{1, 1, 0},
	}
	for _, tc := range cases {
		if got := Allowed(tc.cap, tc.matching); got != tc.want {
			t.Errorf("Allowed(%d,%d)=%d want %d", tc.cap, tc.matching, got, tc.want)
		}
	}
}

func set(pods ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range pods {
		m[p] = true
	}
	return m
}

func TestInScopeCapsAndIsDeterministic(t *testing.T) {
	matching := set("a", "b", "c", "d")
	got := InScope([]string{"d", "b", "c", "a"}, matching, 50)
	if !reflect.DeepEqual(got, set("a", "b")) {
		t.Fatalf("expected the two lowest-named matching pods, got %v", got)
	}
}

func TestInScopeDropsPodsThatNoLongerMatch(t *testing.T) {
	// The controller selected three, but the deployment scaled down and only two match now.
	got := InScope([]string{"a", "b", "gone"}, set("a", "b"), 100)
	if !reflect.DeepEqual(got, set("a", "b")) {
		t.Fatalf("a vanished pod must not be faulted: %v", got)
	}
}

func TestInScopeContainsAnOverSelection(t *testing.T) {
	// If the controller ever selected more than the cap allows, the agent still injects at
	// most the cap.
	got := InScope([]string{"a", "b", "c"}, set("a", "b", "c", "d"), 30)
	if len(got) != 1 || !got["a"] {
		t.Fatalf("cap of 30%% of 4 is one pod, got %v", got)
	}
}

func TestInScopeEmptyWhenCapFloorsToZero(t *testing.T) {
	if got := InScope([]string{"a", "b"}, set("a", "b"), 30); len(got) != 0 {
		t.Fatalf("30%% of two pods is zero pods, got %v", got)
	}
}
