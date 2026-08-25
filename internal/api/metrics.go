package api

import (
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	httpRequests   *prometheus.CounterVec
	httpDuration   *prometheus.HistogramVec
	zerionRequests *prometheus.CounterVec
	zerionDuration *prometheus.HistogramVec
	zerionRetries  *prometheus.CounterVec
	cacheRequests  *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Local HTTP requests.",
		}, []string{"route", "method", "status_class"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "Local HTTP request duration.",
		}, []string{"route", "method"}),
		zerionRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zerion_requests_total",
			Help: "Upstream Zerion HTTP attempts.",
		}, []string{"operation", "result"}),
		zerionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "zerion_request_duration_seconds",
			Help: "Upstream Zerion attempt duration.",
		}, []string{"operation"}),
		zerionRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zerion_retries_total",
			Help: "Upstream Zerion retries.",
		}, []string{"operation", "reason"}),
		cacheRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cache_requests_total",
			Help: "Summary cache lookups.",
		}, []string{"result"}),
	}
	reg.MustRegister(
		m.httpRequests,
		m.httpDuration,
		m.zerionRequests,
		m.zerionDuration,
		m.zerionRetries,
		m.cacheRequests,
	)
	return m
}
