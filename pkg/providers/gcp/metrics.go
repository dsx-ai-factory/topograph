// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcp

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	missingTopologyInfo = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:      "missing_topology_info",
			Subsystem: "topograph_gcp",
			Help:      "Number of times instance topology not found",
		}, []string{"instance_name"},
	)

	missingResourceStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:      "missing_resource_status",
			Subsystem: "topograph_gcp",
			Help:      "Number of times resource status not found",
		}, []string{"instance_name"},
	)

	missingPhysicalHostTopology = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:      "missing_physical_host_topology",
			Subsystem: "topograph_gcp",
			Help:      "Number of times physical host topology not found",
		}, []string{"instance_name"},
	)

	requestLatency = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "request_latency",
			Subsystem:  "topograph_gcp",
			Help:       "Latency of requests",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"method"},
	)
)

func init() {
	prometheus.MustRegister(missingTopologyInfo)
	prometheus.MustRegister(missingResourceStatus)
	prometheus.MustRegister(missingPhysicalHostTopology)
	prometheus.MustRegister(requestLatency)
}
