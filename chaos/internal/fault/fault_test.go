package fault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeExec records commands and answers from a script keyed by a substring of the
// command line.
type fakeExec struct {
	calls   []string
	fail    map[string]string
	started []string
}

func (f *fakeExec) Run(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	for needle, out := range f.fail {
		if strings.Contains(line, needle) {
			return out, errors.New(out)
		}
	}
	return "", nil
}

func (f *fakeExec) Start(_ context.Context, name string, args ...string) (int, error) {
	f.started = append(f.started, name+" "+strings.Join(args, " "))
	return 4242, nil
}

func TestLatencyRunsNetemInsideThePodNamespace(t *testing.T) {
	ex := &fakeExec{}
	ns := Netns{Exec: ex, PID: 1234}
	if err := (Latency{DelayMs: 2000, JitterMs: 50}).Apply(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	want := "nsenter -t 1234 -n -- tc qdisc replace dev eth0 root netem delay 2000ms 50ms"
	if len(ex.calls) != 1 || ex.calls[0] != want {
		t.Fatalf("calls %q", ex.calls)
	}
	if err := (Latency{}).Revert(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	if ex.calls[1] != "nsenter -t 1234 -n -- tc qdisc del dev eth0 root" {
		t.Fatalf("revert %q", ex.calls[1])
	}
}

func TestLatencyRevertIsIdempotent(t *testing.T) {
	ex := &fakeExec{fail: map[string]string{"qdisc del": "Error: Cannot delete qdisc with handle of zero."}}
	if err := (Latency{}).Revert(context.Background(), Netns{Exec: ex, PID: 1}); err != nil {
		t.Fatalf("a missing qdisc is the reverted state: %v", err)
	}
	ex = &fakeExec{fail: map[string]string{"qdisc del": "RTNETLINK answers: Operation not permitted"}}
	if err := (Latency{}).Revert(context.Background(), Netns{Exec: ex, PID: 1}); err == nil {
		t.Fatal("a real failure must surface")
	}
}

func TestPartitionKeepsLoopbackAndTheNodeReachable(t *testing.T) {
	ex := &fakeExec{fail: map[string]string{"-C INPUT": "iptables: Bad rule", "-C OUTPUT": "iptables: Bad rule"}}
	ns := Netns{Exec: ex, PID: 77}
	if err := (Partition{AllowFrom: []string{"172.24.0.2"}}).Apply(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ex.calls, "\n")
	for _, want := range []string{
		"iptables -w -N KILN-CHAOS",
		"iptables -w -A KILN-CHAOS -i lo -j RETURN",
		"iptables -w -A KILN-CHAOS -o lo -j RETURN",
		"iptables -w -A KILN-CHAOS -s 172.24.0.2 -j RETURN",
		"iptables -w -A KILN-CHAOS -d 172.24.0.2 -j RETURN",
		"iptables -w -A KILN-CHAOS -j DROP",
		"iptables -w -I INPUT 1 -j KILN-CHAOS",
		"iptables -w -I OUTPUT 1 -j KILN-CHAOS",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	for _, c := range ex.calls {
		if !strings.HasPrefix(c, "nsenter -t 77 -n -- iptables") {
			t.Errorf("command left the pod namespace: %q", c)
		}
	}
	drop := strings.Index(joined, "-j DROP")
	for _, keep := range []string{"-i lo", "-s 172.24.0.2"} {
		if strings.Index(joined, keep) > drop {
			t.Errorf("%s must be allowed before the DROP", keep)
		}
	}
}

func TestPartitionApplyIsIdempotent(t *testing.T) {
	ex := &fakeExec{fail: map[string]string{"-N KILN-CHAOS": "iptables: Chain already exists."}}
	if err := (Partition{}).Apply(context.Background(), Netns{Exec: ex, PID: 1}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ex.calls, "\n")
	if strings.Contains(joined, "-I INPUT") || strings.Contains(joined, "-I OUTPUT") {
		t.Fatalf("jump rules that already exist (-C succeeded) must not be inserted again:\n%s", joined)
	}
}

func TestPartitionRevertRemovesEveryJumpAndTheChain(t *testing.T) {
	ex := &fakeExec{}
	deletes := 0
	ex.fail = map[string]string{}
	ns := Netns{Exec: &countingExec{fakeExec: ex, deletesBeforeFailing: 2, deletes: &deletes}, PID: 5}
	if err := (Partition{}).Revert(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ex.calls, "\n")
	if strings.Count(joined, "-D INPUT -j KILN-CHAOS") != 3 || strings.Count(joined, "-D OUTPUT -j KILN-CHAOS") != 3 {
		t.Fatalf("expected deletes until none is left:\n%s", joined)
	}
	if !strings.Contains(joined, "-F KILN-CHAOS") || !strings.Contains(joined, "-X KILN-CHAOS") {
		t.Fatalf("chain not flushed and removed:\n%s", joined)
	}
}

// countingExec lets two deletes per hook succeed before reporting nothing left to delete.
type countingExec struct {
	*fakeExec
	deletesBeforeFailing int
	deletes              *int
}

func (c *countingExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	line := strings.Join(args, " ")
	if strings.Contains(line, "-D ") {
		*c.deletes++
		if (*c.deletes-1)%3 == 2 {
			c.fakeExec.calls = append(c.fakeExec.calls, name+" "+line)
			return "iptables: No chain/target/match by that name.", errors.New("iptables: No chain/target/match by that name.")
		}
	}
	return c.fakeExec.Run(ctx, name, args...)
}

func TestPartitionRevertOnACleanPodIsNotAnError(t *testing.T) {
	ex := &fakeExec{fail: map[string]string{"-D": "iptables: No chain/target/match by that name.", "-F": "iptables: No chain/target/match by that name.", "-X": "iptables: No chain/target/match by that name."}}
	if err := (Partition{}).Revert(context.Background(), Netns{Exec: ex, PID: 1}); err != nil {
		t.Fatal(err)
	}
}

func fakeProc(t *testing.T, procs map[int]struct{ cgroup, cmdline string }) string {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte("0::"+p.cgroup+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.ReplaceAll(p.cmdline, " ", "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("1 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPIDByContainerIDPicksTheContainerInit(t *testing.T) {
	const id = "9f1c2a"
	root := fakeProc(t, map[int]struct{ cgroup, cmdline string }{
		1:    {"/init.scope", "systemd"},
		3000: {"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-pod1.slice/cri-containerd-" + id + ".scope", "fortio server"},
		2900: {"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-pod1.slice/cri-containerd-" + id + ".scope", "fortio"},
		3100: {"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-pod2.slice/cri-containerd-other.scope", "envoy"},
	})
	pid, err := PIDByContainerID(root, "containerd://"+id)
	if err != nil || pid != 2900 {
		t.Fatalf("got %d, %v", pid, err)
	}
	if _, err := PIDByContainerID(root, "containerd://missing"); err == nil {
		t.Fatal("expected an error for an unknown container")
	}
}

func TestJoinCgroupWritesThePIDIntoTheTargetsCgroup(t *testing.T) {
	root := fakeProc(t, map[int]struct{ cgroup, cmdline string }{
		500: {"/kubelet.slice/pod/cri-containerd-abc.scope", "app"},
	})
	cgroupRoot := t.TempDir()
	dir := filepath.Join(cgroupRoot, "kubelet.slice", "pod", "cri-containerd-abc.scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte("20000 100000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := JoinCgroup(BurnConfig{ProcRoot: root, CgroupRoot: cgroupRoot, TargetPID: 500, SelfPID: 999})
	if err != nil || got != dir {
		t.Fatalf("got %q, %v", got, err)
	}
	procs, _ := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if string(procs) != "999" {
		t.Fatalf("cgroup.procs = %q", procs)
	}
	cores, err := CPULimit(dir)
	if err != nil || cores != 0.2 {
		t.Fatalf("cores %v, %v", cores, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte("max 100000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CPULimit(dir); err == nil {
		t.Fatal("an unlimited cgroup must be refused")
	}
}

func TestBurnerStartsItselfAsASubprocess(t *testing.T) {
	ex := &fakeExec{}
	until := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	pid, err := (Burner{Exec: ex, Self: "/kiln-chaos", ContainerPID: 500, CPUPercent: 80, MemoryMiB: 64, Until: until}).Start(context.Background())
	if err != nil || pid != 4242 {
		t.Fatalf("pid %d, %v", pid, err)
	}
	want := "/kiln-chaos burn --pid 500 --cpu-percent 80 --memory-mib 64 --until 2026-09-05T12:00:00Z"
	if len(ex.started) != 1 || ex.started[0] != want {
		t.Fatalf("started %q", ex.started)
	}
}

func TestStopBurnerOnlyKillsABurner(t *testing.T) {
	root := fakeProc(t, map[int]struct{ cgroup, cmdline string }{
		700: {"/x", "postgres -D /data"},
	})
	if err := StopBurner(root, 700); err != nil {
		t.Fatalf("a reused pid must be left alone: %v", err)
	}
	if err := StopBurner(root, 701); err != nil {
		t.Fatalf("a gone process is already stopped: %v", err)
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	l := Ledger{Dir: filepath.Join(t.TempDir(), "ledger")}
	e := Entry{Namespace: "shop", Experiment: "kill", ExperimentUID: "e1", Pod: "checkout-1", PodUID: "p1", Kind: "latency-injection", NetnsPID: 12, Deadline: time.Now().Add(time.Minute).UTC().Truncate(time.Second)}
	if err := l.Put(e); err != nil {
		t.Fatal(err)
	}
	got, ok, err := l.Get(e.Key())
	if err != nil || !ok || got != e {
		t.Fatalf("got %+v %v %v", got, ok, err)
	}
	all, err := l.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list %v %v", all, err)
	}
	if err := l.Delete(e.Key()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Get(e.Key()); ok {
		t.Fatal("entry survived delete")
	}
	if err := l.Delete(e.Key()); err != nil {
		t.Fatalf("deleting twice is fine: %v", err)
	}
	if all, err := (Ledger{Dir: filepath.Join(t.TempDir(), "none")}).List(); err != nil || all != nil {
		t.Fatalf("a missing ledger dir is empty: %v %v", all, err)
	}
}
