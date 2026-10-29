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

package oci

import (
	"github.com/prometheus/client_golang/prometheus"
)

var requestLatency = prometheus.NewSummaryVec(
	prometheus.SummaryOpts{
		Name:       "request_latency",
		Help:       "Latency of requests",
		Subsystem:  "topograph_oci",
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
	},
	[]string{"method", "status"},
)

var missingHostData = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name:      "missing_host_data",
		Help:      "Missing host data",
		Subsystem: "topograph_oci",
	},
	[]string{"data", "node_name"},
)

func init() {
	prometheus.MustRegister(requestLatency)
	prometheus.MustRegister(missingHostData)
}
