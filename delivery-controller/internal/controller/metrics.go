package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	decisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kiln_canary_decisions_total",
		Help: "Analysis decisions by action and reason.",
	}, []string{"action", "reason"})
	canaryWeight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kiln_canary_weight_percent",
		Help: "Share of traffic currently routed to the canary.",
	}, []string{"namespace", "rollout"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(decisions, canaryWeight)
}
