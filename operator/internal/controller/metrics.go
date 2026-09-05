package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Feeds the Operator row of METRICS.md: CR creation to Ready.
var timeToReady = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "kiln_tenantdatabase_time_to_ready_seconds",
	Help:    "Seconds from TenantDatabase creation until status.phase first reaches Ready.",
	Buckets: prometheus.ExponentialBuckets(1, 2, 12),
})

func init() {
	metrics.Registry.MustRegister(timeToReady)
}
