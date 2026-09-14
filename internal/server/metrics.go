package server

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type metrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	bytesIn  *prometheus.CounterVec
	bytesOut *prometheus.CounterVec
	// tlsNotAfter is the expiry of the TLS certificate in service (Unix
	// seconds; 0 with TLS off) and tlsReloads counts reload outcomes.
	tlsNotAfter prometheus.Gauge
	tlsReloads  *prometheus.CounterVec
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		requests:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "opens3_s3_requests_total", Help: "S3 API requests by operation and status."}, []string{"op", "status"}),
		latency:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "opens3_s3_request_duration_seconds", Help: "S3 request latency.", Buckets: prometheus.ExponentialBuckets(0.001, 2, 16)}, []string{"op"}),
		bytesIn:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "opens3_s3_received_bytes_total", Help: "Bytes received in request bodies."}, []string{"op"}),
		bytesOut:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "opens3_s3_sent_bytes_total", Help: "Bytes sent in response bodies."}, []string{"op"}),
		tlsNotAfter: prometheus.NewGauge(prometheus.GaugeOpts{Name: "opens3_tls_certificate_not_after_seconds", Help: "Expiry of the TLS certificate in service as a Unix timestamp (0 when TLS is off)."}),
		tlsReloads:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "opens3_tls_certificate_reloads_total", Help: "TLS certificate reloads by result (ok, unchanged, error)."}, []string{"result"}),
	}
	reg.MustRegister(m.requests, m.latency, m.bytesIn, m.bytesOut, m.tlsNotAfter, m.tlsReloads, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *metrics) observe(op string, status int, dur time.Duration, in, out int64) {
	m.requests.WithLabelValues(op, strconv.Itoa(status)).Inc()
	m.latency.WithLabelValues(op).Observe(dur.Seconds())
	m.bytesIn.WithLabelValues(op).Add(float64(in))
	m.bytesOut.WithLabelValues(op).Add(float64(out))
}
