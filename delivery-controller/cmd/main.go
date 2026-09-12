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
	"github.com/trnahnh/kiln/audit/outbox"
	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
	"github.com/trnahnh/kiln/delivery-controller/internal/controller"
	"github.com/trnahnh/kiln/delivery-controller/internal/mesh"
	"github.com/trnahnh/kiln/slo"
	"github.com/trnahnh/kiln/tracing"
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
	var metricsAddr, probeAddr, prometheusURL, auditBrokers, auditTopic, otlpEndpoint string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to; 0 disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.StringVar(&prometheusURL, "prometheus-url", "http://prometheus.monitoring.svc:9090", "Prometheus base URL the analysis reads canary metrics from.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election so only one manager reconciles.")
	flag.StringVar(&auditBrokers, "audit-brokers", "", "Comma-separated Kafka brokers audit events are published to; empty disables publishing.")
	flag.StringVar(&auditTopic, "audit-topic", audit.Topic, "Kafka topic audit events are published to.")
	flag.StringVar(&otlpEndpoint, "otlp-endpoint", "", "OTLP gRPC endpoint spans are exported to; empty disables tracing export.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	stopTracing, err := tracing.Setup(context.Background(), "kiln-delivery-controller", otlpEndpoint)
	if err != nil {
		setupLog.Error(err, "failed to start tracing")
		os.Exit(1)
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = stopTracing(flush)
	}()

	// Plain HTTP metrics: Prometheus scrapes by pod annotation (ADR-0001).
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "canaryrollout.platform.internal",
	})
	if err != nil {
		setupLog.Error(err, "failed to start manager")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("canaryrollout")
	drainer, err := newOutbox(mgr, recorder, auditBrokers, auditTopic)
	if err != nil {
		setupLog.Error(err, "failed to start the audit outbox")
		os.Exit(1)
	}

	if err := (&controller.CanaryRolloutReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: recorder,
		Metrics:  slo.NewPrometheus(prometheusURL),
		Router:   &mesh.Istio{Client: mgr.GetClient()},
		Outbox:    drainer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to create controller", "controller", "canaryrollout")
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

	setupLog.Info("starting manager", "prometheus", prometheusURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

// newOutbox wires the audit outbox (ADR-0022): the drainer delivers what each reconcile
// committed in CanaryRollout status and clears it on the broker's acknowledgement. A backlog
// past the threshold surfaces as a Warning Event on the CanaryRollout it belongs to.
func newOutbox(mgr manager.Manager, recorder record.EventRecorder, brokers, topic string) (*audit.Drainer, error) {
	var deliverer audit.Deliverer = audit.Discard{}
	if brokers == "" {
		setupLog.Info("audit publishing disabled: no --audit-brokers")
	} else {
		pub, err := audit.NewKafka(audit.Options{
			Brokers:    strings.Split(brokers, ","),
			Topic:      topic,
			Registerer: ctrlmetrics.Registry,
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
		deliverer = pub
	}
	drainer, err := audit.NewDrainer(audit.DrainerOptions{
		Deliverer:  deliverer,
		Registerer: ctrlmetrics.Registry,
		Store: outbox.Store[platformv1.CanaryRollout, *platformv1.CanaryRollout]{
			Client: mgr.GetClient(),
			List:   func(obj *platformv1.CanaryRollout) *[]audit.Pending { return &obj.Status.Audit.Pending },
		},
		OnBacklog: func(key string, pending int) {
			ns, name, _ := strings.Cut(key, "/")
			obj := &platformv1.CanaryRollout{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
			recorder.Eventf(obj, corev1.EventTypeWarning, "AuditOutboxBacklog", "%d audit events await Kafka", pending)
		},
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.Add(drainer); err != nil {
		return nil, err
	}
	return drainer, nil
}
