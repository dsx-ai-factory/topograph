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

package gpunicrail

import (
	"context"
	"time"

	"k8s.io/klog/v2"
)

const (
	initialBackoff = 10 * time.Second
	maxBackoff     = 5 * time.Minute
)

// Loop refreshes the Node's record until ctx ends.
type Loop struct {
	Collector *Collector
	Publisher *Publisher
	Interval  time.Duration
}

// Run reconciles once, then every Interval. A failed refresh keeps the last
// published record and is logged and retried with backoff.
func (l *Loop) Run(ctx context.Context) {
	klog.InfoS("Starting GPU/NIC rail mapping refresh loop", "node", l.Publisher.Node.Name, "interval", l.Interval)
	backoff := initialBackoff
	for {
		wait := l.Interval
		if err := l.Once(ctx); err != nil {
			wait = min(backoff, l.Interval)
			klog.ErrorS(err, "GPU/NIC rail mapping refresh failed; keeping the previous record", "node", l.Publisher.Node.Name, "retryAfter", wait)
			backoff = min(backoff*2, maxBackoff)
		} else {
			backoff = initialBackoff
		}
		select {
		case <-ctx.Done():
			klog.InfoS("Stopped GPU/NIC rail mapping refresh loop", "node", l.Publisher.Node.Name)
			return
		case <-time.After(wait):
		}
	}
}

// Once collects and publishes, or deletes the record when no rail interface
// is selected.
func (l *Loop) Once(ctx context.Context) error {
	started := time.Now()
	klog.InfoS("Refreshing GPU/NIC rail mapping", "node", l.Publisher.Node.Name)
	status, err := l.Collector.Collect(ctx)
	if err != nil {
		return err
	}
	if status == nil {
		klog.InfoS("No rail interfaces selected; removing GPU/NIC rail mapping", "node", l.Publisher.Node.Name)
		return l.Publisher.Delete(ctx)
	}
	if err := l.Publisher.Publish(ctx, status); err != nil {
		return err
	}
	klog.InfoS("Completed GPU/NIC rail mapping refresh", "node", l.Publisher.Node.Name, "nics", len(status.NICs), "elapsed", time.Since(started))
	return nil
}
