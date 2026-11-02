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

package node_observer

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// IgnoreCurrent captures goroutines running before m.Run() (klog flushDaemon,
	// client-go SharedIndexInformer goroutines initialized at import time).
	//
	// The workqueue delayingType.waitingLoop filter covers a known async-shutdown
	// window: tests call informer.Stop() which calls queue.ShutDown(), but
	// queue.ShutDown() does not synchronously drain the background waitingLoop
	// goroutine — it only signals it. Goleak fires before the goroutine exits its
	// final select iteration. This is not a test-scoped resource leak.
	goleak.VerifyTestMain(m,
		goleak.IgnoreCurrent(),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
	)
}
