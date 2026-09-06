package main

import (
	"context"
	"flag"
	"os"
	"strings"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
	"github.com/trnahnh/kiln/operator/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr, auditBrokers, auditTopic string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to; 0 disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election so only one manager reconciles.")
	flag.StringVar(&auditBrokers, "audit-brokers", "", "Comma-separated Kafka brokers audit events are published to; empty disables publishing.")
	flag.StringVar(&auditTopic, "audit-topic", audit.Topic, "Kafka topic audit events are published to.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Plain HTTP metrics: Prometheus scrapes by pod annotation (ADR-0001).
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "tenantdatabase.platform.internal",
	})
	if err != nil {
		setupLog.Error(err, "failed to start manager")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("tenantdatabase")
	publisher, err := newPublisher(mgr, recorder, auditBrokers, auditTopic)
	if err != nil {
		setupLog.Error(err, "failed to start the audit publisher")
		os.Exit(1)
	}

	if err := (&controller.TenantDatabaseReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: recorder,
		Audit:    publisher,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to create controller", "controller", "tenantdatabase")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

// newPublisher wires the non-blocking audit producer (ADR-0017); a dropped event surfaces
// as a Warning Event on the TenantDatabase it was about.
func newPublisher(mgr manager.Manager, recorder record.EventRecorder, brokers, topic string) (audit.Publisher, error) {
	if brokers == "" {
		setupLog.Info("audit publishing disabled: no --audit-brokers")
		return audit.Discard{}, nil
	}
	pub, err := audit.NewKafka(audit.Options{
		Brokers:    strings.Split(brokers, ","),
		Topic:      topic,
		Registerer: ctrlmetrics.Registry,
		OnFailure: func(e audit.Event, err error) {
			parts := strings.SplitN(e.Resource, "/", 3)
			if len(parts) != 3 {
				return
			}
			obj := &platformv1.TenantDatabase{ObjectMeta: metav1.ObjectMeta{Namespace: parts[1], Name: parts[2]}}
			recorder.Eventf(obj, corev1.EventTypeWarning, "AuditPublishFailed", "%s event not published: %v", e.Action, err)
		},
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		flush, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return pub.Close(flush)
	})); err != nil {
		return nil, err
	}
	return pub, nil
}
