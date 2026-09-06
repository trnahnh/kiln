package fault

import (
	"context"
	"fmt"
	"strings"
)

const (
	podInterface = "eth0"
	chain        = "KILN-CHAOS"
)

// Latency delays every packet the pod sends, so callers see it on their responses.
type Latency struct {
	DelayMs  int
	JitterMs int
}

func (l Latency) Apply(ctx context.Context, ns Netns) error {
	_, err := ns.run(ctx, "tc", "qdisc", "replace", "dev", podInterface, "root", "netem", "delay", fmt.Sprintf("%dms", l.DelayMs), fmt.Sprintf("%dms", l.JitterMs))
	return err
}

func (l Latency) Revert(ctx context.Context, ns Netns) error {
	out, err := ns.run(ctx, "tc", "qdisc", "del", "dev", podInterface, "root")
	if err != nil && !alreadyClean(out) {
		return err
	}
	return nil
}

// tc reports these when there is no netem qdisc to delete, which is the state we want.
func alreadyClean(out string) bool {
	return strings.Contains(out, "Cannot delete qdisc with handle of zero") ||
		strings.Contains(out, "Cannot find device") ||
		strings.Contains(out, "No such file or directory") ||
		strings.Contains(out, "Invalid argument")
}

// Partition drops everything the pod sends or receives except loopback, where its own
// sidecar lives, and the node itself, so kubelet probes and exec keep working.
type Partition struct {
	AllowFrom []string
}

func (p Partition) Apply(ctx context.Context, ns Netns) error {
	if _, err := ns.run(ctx, "iptables", "-w", "-N", chain); err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	if _, err := ns.run(ctx, "iptables", "-w", "-F", chain); err != nil {
		return err
	}
	rules := [][]string{{"-i", "lo", "-j", "RETURN"}, {"-o", "lo", "-j", "RETURN"}}
	for _, ip := range p.AllowFrom {
		rules = append(rules, []string{"-s", ip, "-j", "RETURN"}, []string{"-d", ip, "-j", "RETURN"})
	}
	rules = append(rules, []string{"-j", "DROP"})
	for _, r := range rules {
		if _, err := ns.run(ctx, "iptables", append([]string{"-w", "-A", chain}, r...)...); err != nil {
			return err
		}
	}
	for _, hook := range []string{"INPUT", "OUTPUT"} {
		if _, err := ns.run(ctx, "iptables", "-w", "-C", hook, "-j", chain); err == nil {
			continue
		}
		if _, err := ns.run(ctx, "iptables", "-w", "-I", hook, "1", "-j", chain); err != nil {
			return err
		}
	}
	return nil
}

func (p Partition) Revert(ctx context.Context, ns Netns) error {
	for _, hook := range []string{"INPUT", "OUTPUT"} {
		for {
			if _, err := ns.run(ctx, "iptables", "-w", "-D", hook, "-j", chain); err != nil {
				break
			}
		}
	}
	if _, err := ns.run(ctx, "iptables", "-w", "-F", chain); err != nil && !noChain(err) {
		return err
	}
	if _, err := ns.run(ctx, "iptables", "-w", "-X", chain); err != nil && !noChain(err) {
		return err
	}
	return nil
}

func noChain(err error) bool {
	return strings.Contains(err.Error(), "No chain") || strings.Contains(err.Error(), "does not exist")
}
