// Package fault applies and reverts one fault on one pod from the node the pod runs on.
// Every injector is idempotent in both directions so an agent can retry or replay a
// ledger without knowing what already happened.
package fault

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Exec runs node commands. The agent uses the host; tests record what would have run.
type Exec interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	// Start launches a long-running process and returns its PID without waiting.
	Start(ctx context.Context, name string, args ...string) (int, error)
}

type Host struct{}

func (Host) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (Host) Start(_ context.Context, name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	// Reap it when it ends so a finished burner is not left as a zombie.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// Netns runs commands inside the network namespace of a process on this node.
type Netns struct {
	Exec Exec
	PID  int
}

func (n Netns) run(ctx context.Context, name string, args ...string) (string, error) {
	full := append([]string{"-t", strconv.Itoa(n.PID), "-n", "--", name}, args...)
	return n.Exec.Run(ctx, "nsenter", full...)
}
