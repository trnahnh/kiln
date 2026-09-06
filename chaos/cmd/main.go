package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/chaos/internal/agent"
	"github.com/trnahnh/kiln/chaos/internal/controller"
	"github.com/trnahnh/kiln/chaos/internal/fault"
	"github.com/trnahnh/kiln/slo"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1.AddToScheme(scheme))
}

// One binary, three modes: the controller Deployment, the per-node agent DaemonSet, and
// the short-lived CPU/memory burner the agent forks for resource-exhaustion.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "burn" {
		if err := runBurner(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "burn:", err)
			os.Exit(1)
		}
		return
	}

	var mode, metricsAddr, probeAddr, prometheusURL, ledgerDir string
	flag.StringVar(&mode, "mode", "controller", "controller or agent")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to; 0 disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.StringVar(&prometheusURL, "prometheus-url", "http://prometheus.monitoring.svc:9090", "Prometheus base URL the SLO windows are read from.")
	flag.StringVar(&ledgerDir, "ledger-dir", "/var/lib/kiln-chaos", "Where the agent records live faults so a restart reverts them.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	switch mode {
	case "controller":
		runController(metricsAddr, probeAddr, prometheusURL)
	case "agent":
		runAgent(metricsAddr, probeAddr, ledgerDir)
	default:
		setupLog.Info("unknown mode", "mode", mode)
		os.Exit(1)
	}
}

func runController(metricsAddr, probeAddr, prometheusURL string) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         true,
		LeaderElectionID:       "chaosexperiment.platform.internal",
	})
	if err != nil {
		setupLog.Error(err, "failed to start manager")
		os.Exit(1)
	}
	if err := (&controller.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("chaos-controller"),
		Metrics:  slo.NewPrometheus(prometheusURL),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to create the chaos controller")
		os.Exit(1)
	}
	addProbes(mgr)
	setupLog.Info("starting the chaos controller", "prometheus", prometheusURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

func runAgent(metricsAddr, probeAddr, ledgerDir string) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))
	node := os.Getenv("NODE_NAME")
	if node == "" {
		setupLog.Info("NODE_NAME is required for the agent")
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		setupLog.Error(err, "cannot resolve own binary path")
		os.Exit(1)
	}
	// The agent only ever needs pods on its own node and the experiments; scope the cache
	// to keep it cheap on large clusters.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		Cache:                  cache.Options{},
	})
	if err != nil {
		setupLog.Error(err, "failed to start manager")
		os.Exit(1)
	}
	led := fault.Ledger{Dir: ledgerDir}
	injector := agent.HostInjector{
		Exec: fault.Host{}, Ledger: led,
		ProcRoot: "/proc", CgroupRoot: "/sys/fs/cgroup",
		SelfBinary: self, SelfPID: os.Getpid(),
	}
	rec := &agent.Reconciler{
		Client:   mgr.GetClient(),
		Injector: injector,
		Ledger:   led,
		Recorder: mgr.GetEventRecorderFor("chaos-agent"),
		NodeName: node,
		NodeIP:   os.Getenv("NODE_IP"),
	}
	// A fresh agent reverts whatever its predecessor left before serving; those faults'
	// experiments will re-inject through the normal loop if still live.
	if err := revertLedgerOnStartup(led, injector); err != nil {
		setupLog.Error(err, "failed to revert the ledger on startup")
		os.Exit(1)
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to create the chaos agent")
		os.Exit(1)
	}
	addProbes(mgr)
	setupLog.Info("starting the chaos agent", "node", node)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

func revertLedgerOnStartup(led fault.Ledger, injector agent.Injector) error {
	entries, err := led.List()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := injector.Revert(context.Background(), e); err != nil {
			return err
		}
		if err := led.Delete(e.Key()); err != nil {
			return err
		}
	}
	return nil
}

func runBurner(args []string) error {
	fs := flag.NewFlagSet("burn", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "target container PID whose cgroup to join")
	cpuPercent := fs.Int("cpu-percent", 100, "share of the container CPU limit to contend for")
	memoryMiB := fs.Int("memory-mib", 0, "memory to allocate inside the cgroup")
	until := fs.String("until", "", "RFC3339 time to stop, an ultimate self-terminate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	deadline, err := time.Parse(time.RFC3339, *until)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}
	return fault.Burn(context.Background(), fault.BurnConfig{
		ProcRoot: "/proc", CgroupRoot: "/sys/fs/cgroup",
		TargetPID: *pid, SelfPID: os.Getpid(),
		CPUPercent: *cpuPercent, MemoryMiB: *memoryMiB, Until: deadline,
	})
}

func addProbes(mgr ctrl.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "failed to set up ready check")
		os.Exit(1)
	}
}
