/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package metrics

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dsx-ai-factory/topograph/internal/version"
)

var (
	versionInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:      "version",
			Help:      "Topograph version",
			Subsystem: "topograph",
		},
		[]string{"version"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Subsystem: "topograph",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "path", "proto", "from", "status"},
	)

	topologyRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:      "request_duration_seconds",
			Help:      "Topology request duration in seconds.",
			Subsystem: "topograph",
			Buckets:   []float64{1, 2.5, 5, 7.5, 10, 12.5, 15, 17.5, 20, 25, 30},
		},
		[]string{"provider", "engine", "status"},
	)

	missingTopologyNodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:      "missing_topology",
			Help:      "Nodes with missing topology information.",
			Subsystem: "topograph",
		},
		[]string{"provider", "node"},
	)

	validationErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:      "validation_error_total",
			Help:      "Total number of validation errors.",
			Subsystem: "topograph",
		},
		[]string{"type"},
	)
)

func init() {
	prometheus.MustRegister(versionInfo)
	prometheus.MustRegister(httpRequestDuration)
	prometheus.MustRegister(topologyRequestDuration)
	prometheus.MustRegister(missingTopologyNodes)
	prometheus.MustRegister(validationErrorsTotal)
}

func AddHttpRequest(method, path, proto, from string, code int, duration time.Duration) {
	status := fmt.Sprintf("%d", code)
	httpRequestDuration.WithLabelValues(method, path, proto, from, status).Observe(duration.Seconds())
	versionInfo.WithLabelValues(version.Version).Set(1)
}

func AddTopologyRequest(provider, engine string, code int, duration time.Duration) {
	status := fmt.Sprintf("%d", code)
	topologyRequestDuration.WithLabelValues(provider, engine, status).Observe(duration.Seconds())
}

func SetMissingTopology(provider, nodename string) {
	missingTopologyNodes.WithLabelValues(provider, nodename).Set(1.0)
}

func AddValidationError(errorType string) {
	validationErrorsTotal.WithLabelValues(errorType).Inc()
}
