package agent

import (
	"context"
	"fmt"
	"time"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/chaos/internal/fault"
)

// Request is everything the injector needs to put one fault on one pod, resolved from the
// experiment and the live pod so the injector itself touches no Kubernetes API.
type Request struct {
	Namespace     string
	Experiment    string
	ExperimentUID string
	Pod           string
	PodUID        string
	ContainerID   string
	FaultType     platformv1.FaultType
	LatencyMs     int
	JitterMs      int
	CPUPercent    int
	MemoryMiB     int
	AllowFrom     []string
	// Deadline is the lease expiry: the agent's dead-man switch reverts the fault if the
	// controller stops renewing it. Refreshed on every reconcile.
	Deadline time.Time
	// BurnUntil is the experiment's own end; a resource-exhaustion burner self-terminates
	// then even if every agent and the controller are gone.
	BurnUntil time.Time
}

func (r Request) entry(netnsPID, burnerPID int) fault.Entry {
	return fault.Entry{
		Namespace: r.Namespace, Experiment: r.Experiment, ExperimentUID: r.ExperimentUID,
		Pod: r.Pod, PodUID: r.PodUID, Kind: string(r.FaultType),
		NetnsPID: netnsPID, BurnerPID: burnerPID, Deadline: r.Deadline, AppliedAt: time.Now().UTC(),
	}
}

// Injector applies and reverts faults. The real one drives tc/iptables/cgroups; tests
// substitute a recorder so the agent's decisions are exercised without a node.
type Injector interface {
	Apply(ctx context.Context, r Request) (fault.Entry, error)
	Revert(ctx context.Context, e fault.Entry) error
}

// HostInjector resolves the target's host PID and runs the fault packages against it.
type HostInjector struct {
	Exec       fault.Exec
	Ledger     fault.Ledger
	ProcRoot   string
	CgroupRoot string
	SelfBinary string
	SelfPID    int
}

func (h HostInjector) Apply(ctx context.Context, r Request) (fault.Entry, error) {
	pid, err := fault.PIDByContainerID(h.ProcRoot, r.ContainerID)
	if err != nil {
		return fault.Entry{}, err
	}
	ns := fault.Netns{Exec: h.Exec, PID: pid}
	switch r.FaultType {
	case platformv1.FaultLatencyInjection:
		if err := (fault.Latency{DelayMs: r.LatencyMs, JitterMs: r.JitterMs}).Apply(ctx, ns); err != nil {
			return fault.Entry{}, err
		}
		return r.entry(pid, 0), nil
	case platformv1.FaultNetworkPartition:
		if err := (fault.Partition{AllowFrom: r.AllowFrom}).Apply(ctx, ns); err != nil {
			return fault.Entry{}, err
		}
		return r.entry(pid, 0), nil
	case platformv1.FaultResourceExhaustion:
		burnerPID, err := fault.Burner{
			Exec: h.Exec, Self: h.SelfBinary, ContainerPID: pid,
			CPUPercent: r.CPUPercent, MemoryMiB: r.MemoryMiB, Until: r.BurnUntil,
		}.Start(ctx)
		if err != nil {
			return fault.Entry{}, err
		}
		return r.entry(pid, burnerPID), nil
	default:
		return fault.Entry{}, fmt.Errorf("agent does not inject %q", r.FaultType)
	}
}

func (h HostInjector) Revert(ctx context.Context, e fault.Entry) error {
	ns := fault.Netns{Exec: h.Exec, PID: e.NetnsPID}
	switch platformv1.FaultType(e.Kind) {
	case platformv1.FaultLatencyInjection:
		return fault.Latency{}.Revert(ctx, ns)
	case platformv1.FaultNetworkPartition:
		return fault.Partition{}.Revert(ctx, ns)
	case platformv1.FaultResourceExhaustion:
		return fault.StopBurner(h.ProcRoot, e.BurnerPID)
	default:
		return fmt.Errorf("unknown fault kind %q in ledger", e.Kind)
	}
}
