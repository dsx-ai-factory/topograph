// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package k8s

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

const (
	envKubeQPS   = "KUBE_QPS"
	envKubeBurst = "KUBE_BURST"
)

// ConfigureClientRateLimits applies client-go rate limits from the environment.
// When only one value is set, the other uses the client-go default. The
// explicit limiter is shared by every client created from config.
func ConfigureClientRateLimits(config *rest.Config) error {
	qps, hasQPS, err := envFloat32(envKubeQPS)
	if err != nil {
		return err
	}
	burst, hasBurst, err := envInt(envKubeBurst)
	if err != nil {
		return err
	}
	if !hasQPS && !hasBurst {
		return nil
	}

	qps, burst = normalizedClientRateLimits(qps, burst)
	config.QPS = qps
	config.Burst = burst
	config.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, burst)
	return nil
}

func envFloat32(name string) (float32, bool, error) {
	value, ok := os.LookupEnv(name)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseFloat(value, 32)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, false, fmt.Errorf("%s must be a non-negative number", name)
	}
	return float32(parsed), true, nil
}

func envInt(name string) (int, bool, error) {
	value, ok := os.LookupEnv(name)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 0)
	if err != nil || parsed < 0 {
		return 0, false, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return int(parsed), true, nil
}

func normalizedClientRateLimits(qps float32, burst int) (float32, int) {
	if qps == 0 {
		qps = rest.DefaultQPS
	}
	if burst == 0 {
		burst = rest.DefaultBurst
	}
	return qps, burst
}
