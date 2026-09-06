package fault

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Burner competes for the target container's CPU quota from inside its cgroup, so the
// kernel's limit throttles the container and nothing else on the node. The agent runs with
// the host PID namespace, so a burner started as a child has a host PID and can move itself
// into the target's cgroup by writing that PID.
type Burner struct {
	Exec         Exec
	Self         string
	ContainerPID int
	CPUPercent   int
	MemoryMiB    int
	Until        time.Time
}

func (b Burner) Start(ctx context.Context) (int, error) {
	return b.Exec.Start(ctx, b.Self, "burn",
		"--pid", strconv.Itoa(b.ContainerPID),
		"--cpu-percent", strconv.Itoa(b.CPUPercent),
		"--memory-mib", strconv.Itoa(b.MemoryMiB),
		"--until", b.Until.UTC().Format(time.RFC3339))
}

// StopBurner kills a burner by PID, but only a process that still is one: after an agent
// restart the PID could have been reused.
func StopBurner(procRoot string, pid int) error {
	cmd := Cmdline(procRoot, pid)
	if cmd == "" {
		return nil
	}
	if !strings.Contains(cmd, " burn ") && !strings.HasSuffix(cmd, " burn") {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := p.Kill(); err != nil && !strings.Contains(err.Error(), "already finished") {
		return fmt.Errorf("killing burner %d: %w", pid, err)
	}
	return nil
}

type BurnConfig struct {
	ProcRoot   string
	CgroupRoot string
	TargetPID  int
	SelfPID    int
	CPUPercent int
	MemoryMiB  int
	Until      time.Time
}

// JoinCgroup moves the calling process into the cgroup of the target process and returns
// the cgroup's directory.
func JoinCgroup(cfg BurnConfig) (string, error) {
	path, err := CgroupPath(cfg.ProcRoot, cfg.TargetPID)
	if err != nil {
		return "", fmt.Errorf("target cgroup: %w", err)
	}
	dir := filepath.Join(cfg.CgroupRoot, path)
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(cfg.SelfPID)), 0o644); err != nil {
		return "", fmt.Errorf("joining %s: %w", dir, err)
	}
	return dir, nil
}

// CPULimit reads the cgroup's cpu.max as a number of cores; an unlimited cgroup is an
// error because a burner in it would contend with the whole node.
func CPULimit(dir string) (float64, error) {
	b, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		return 0, fmt.Errorf("malformed cpu.max %q", strings.TrimSpace(string(b)))
	}
	if fields[0] == "max" {
		return 0, fmt.Errorf("container has no CPU limit")
	}
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("cpu.max quota %q: %w", fields[0], err)
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || period <= 0 {
		return 0, fmt.Errorf("cpu.max period %q", fields[1])
	}
	return quota / period, nil
}

// Burn joins the target's cgroup and holds CPU and memory there until the deadline or the
// context ends. The spin is spread over enough threads to cover the limit, each busy for
// its share of every 100 ms slice.
func Burn(ctx context.Context, cfg BurnConfig) error {
	dir, err := JoinCgroup(cfg)
	if err != nil {
		return err
	}
	cores, err := CPULimit(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, cfg.Until)
	defer cancel()

	hold := make([]byte, cfg.MemoryMiB<<20)
	for i := 0; i < len(hold); i += 4096 {
		hold[i] = 1
	}

	want := cores * float64(cfg.CPUPercent) / 100
	workers := int(math.Max(1, math.Ceil(want)))
	duty := want / float64(workers)
	for range workers {
		go spin(ctx, duty)
	}
	<-ctx.Done()
	runtime.KeepAlive(hold)
	return nil
}

func spin(ctx context.Context, duty float64) {
	runtime.LockOSThread()
	const slice = 100 * time.Millisecond
	busy := time.Duration(float64(slice) * duty)
	for ctx.Err() == nil {
		end := time.Now().Add(busy)
		for time.Now().Before(end) {
		}
		if rest := slice - busy; rest > 0 {
			time.Sleep(rest)
		}
	}
}
