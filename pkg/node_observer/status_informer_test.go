/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package node_observer

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestNewStatusInformer(t *testing.T) {
	ctx := context.TODO()
	trigger := &Trigger{
		NodeSelector: map[string]string{"key": "val"},
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"key": "val"},
		},
	}
	apiServer := &APIServer{
		Namespace: "topograph",
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "topograph"},
		},
		ContainerName: "topograph",
	}
	informer, err := NewStatusInformer(ctx, nil, trigger, apiServer, "topograph-node-data-broker", "topograph", 0, nil)
	require.NoError(t, err)
	t.Cleanup(func() { informer.Stop(nil) })
	require.NotNil(t, informer.nodeFactory)
	require.NotNil(t, informer.podFactory)
	require.NotNil(t, informer.apiFactory)
	require.NotNil(t, informer.brokerFactory)
	require.Equal(t, "topograph", informer.apiServerContainerName)
}

func TestNewStatusInformerBrokerGateRequiresNameAndNamespace(t *testing.T) {
	testCases := []struct {
		name            string
		brokerName      string
		brokerNamespace string
	}{
		{name: "neither is set"},
		{name: "name only", brokerName: "topograph-node-data-broker"},
		{name: "namespace only", brokerNamespace: "topograph"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			informer, err := NewStatusInformer(context.TODO(), nil, nil, nil, tc.brokerName, tc.brokerNamespace, 0, nil)
			require.NoError(t, err)
			t.Cleanup(func() { informer.Stop(nil) })
			require.Nil(t, informer.brokerFactory)
		})
	}
}

func TestNodeInformerPreservesMainBranchTriggers(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	s, err := NewStatusInformer(
		context.Background(),
		client,
		&Trigger{NodeSelector: map[string]string{"role": "compute"}},
		nil,
		"",
		"",
		time.Second,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() { s.Stop(nil) })
	require.NoError(t, s.startNodeInformer())

	node, err := client.CoreV1().Nodes().Create(context.Background(), &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"role": "compute"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return s.queue.Len() == 1 }, time.Second, 10*time.Millisecond)

	key, shutdown := s.queue.Get()
	require.False(t, shutdown)
	s.queue.Done(key)
	s.queue.Forget(key)
	require.Zero(t, s.queue.Len())

	updated := node.DeepCopy()
	updated.Annotations = map[string]string{"example.com/input": "changed"}
	_, err = client.CoreV1().Nodes().Update(context.Background(), updated, metav1.UpdateOptions{})
	require.NoError(t, err)

	informer := s.nodeFactory.Core().V1().Nodes().Informer()
	require.Eventually(t, func() bool {
		obj, exists, getErr := informer.GetStore().GetByKey(node.Name)
		if getErr != nil || !exists {
			return false
		}
		cachedNode, ok := obj.(*corev1.Node)
		return ok && cachedNode.Annotations["example.com/input"] == "changed"
	}, time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return s.queue.Len() != 0 }, 100*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, client.CoreV1().Nodes().Delete(context.Background(), node.Name, metav1.DeleteOptions{}))
	require.Eventually(t, func() bool { return s.queue.Len() == 1 }, time.Second, 10*time.Millisecond)
}

func TestBrokerDaemonSetReady(t *testing.T) {
	testCases := []struct {
		name    string
		desired int32
		ready   int32
		want    bool
	}{
		{name: "all desired replicas ready", desired: 3, ready: 3, want: true},
		{name: "replicas still becoming ready", desired: 3, ready: 2, want: false},
		{name: "no replicas desired", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			daemonSet := &appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: tc.desired,
				NumberReady:            tc.ready,
			}}
			require.Equal(t, tc.want, isBrokerDaemonSetReady(daemonSet))
		})
	}
}

func TestBrokerDaemonSetReadyRejectsZeroDesiredReplicas(t *testing.T) {
	daemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name:      "topograph-node-data-broker",
		Namespace: "topograph",
	}}

	ready, err := brokerDaemonSetReady(daemonSet)
	require.False(t, ready)
	require.EqualError(t, err, "node-data-broker DaemonSet topograph/topograph-node-data-broker has 0 desired replicas; check its node selector, affinity, and tolerations")
}

func TestAPIServerPodUpdateTriggersOnReadyTransitionAndRestart(t *testing.T) {
	testCases := []struct {
		name      string
		oldPod    *corev1.Pod
		newPod    *corev1.Pod
		triggered bool
	}{
		{
			name:      "not ready after update",
			oldPod:    makeWorkloadPod(false, makeContainerStatus("topograph", false, 0)),
			newPod:    makeWorkloadPod(false, makeContainerStatus("topograph", false, 0)),
			triggered: false,
		},
		{
			name:      "becomes ready",
			oldPod:    makeWorkloadPod(false, makeContainerStatus("topograph", false, 0)),
			newPod:    makeWorkloadPod(true, makeContainerStatus("topograph", true, 0)),
			triggered: true,
		},
		{
			name:      "target container restart count increases while ready",
			oldPod:    makeWorkloadPod(true, makeContainerStatus("topograph", true, 1)),
			newPod:    makeWorkloadPod(true, makeContainerStatus("topograph", true, 2)),
			triggered: true,
		},
		{
			name:      "ready update without restart",
			oldPod:    makeWorkloadPod(true, makeContainerStatus("topograph", true, 1)),
			newPod:    makeWorkloadPod(true, makeContainerStatus("topograph", true, 1)),
			triggered: false,
		},
		{
			name: "sidecar restart does not trigger",
			oldPod: makeWorkloadPod(true,
				makeContainerStatus("topograph", true, 1),
				makeContainerStatus("sidecar", true, 1),
			),
			newPod: makeWorkloadPod(true,
				makeContainerStatus("topograph", true, 1),
				makeContainerStatus("sidecar", true, 2),
			),
			triggered: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.triggered, shouldRequestOnAPIServerUpdate(tc.oldPod, tc.newPod, "topograph"))
		})
	}
}

func TestAPIServerPodReadinessRequiresTargetContainer(t *testing.T) {
	require.True(t, isAPIServerPodReady(
		makeWorkloadPod(true, makeContainerStatus("topograph", true, 0)),
		"topograph",
	))
	require.False(t, isAPIServerPodReady(
		makeWorkloadPod(true, makeContainerStatus("topograph", false, 0)),
		"topograph",
	))
	require.False(t, isAPIServerPodReady(
		makeWorkloadPod(true, makeContainerStatus("sidecar", true, 0)),
		"topograph",
	))
}

func TestRequestOnDeleteQueuesRequest(t *testing.T) {
	pod := makeWorkloadPod(true, makeContainerStatus("topograph", true, 0))
	testCases := []struct {
		name string
		obj  any
	}{
		{
			name: "pod",
			obj:  pod,
		},
		{
			name: "tombstone containing pod",
			obj: cache.DeletedFinalStateUnknown{
				Key: "topograph/api-server",
				Obj: pod,
			},
		},
		{
			name: "tombstone without an object",
			obj: cache.DeletedFinalStateUnknown{
				Key: "topograph/api-server",
			},
		},
		{
			name: "tombstone containing unexpected object",
			obj: cache.DeletedFinalStateUnknown{
				Key: "topograph/api-server",
				Obj: &appsv1.DaemonSet{},
			},
		},
		{
			name: "object with no resolvable key",
			obj:  "unexpected",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStatusInformer(t, time.Second, nil)

			s.requestOnDelete("API server pod")(tc.obj)

			require.Equal(t, 1, s.queue.Len())
		})
	}
}

func TestAPIServerInformerRegistersDeleteHandler(t *testing.T) {
	pod := makeWorkloadPod(false, makeContainerStatus("topograph", false, 0))
	client := k8sfake.NewSimpleClientset(pod)
	s, err := NewStatusInformer(
		context.Background(),
		client,
		nil,
		&APIServer{
			Namespace:     pod.Namespace,
			PodSelector:   &metav1.LabelSelector{},
			ContainerName: "topograph",
		},
		"",
		"",
		0,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		s.Stop(nil)
	})

	require.NoError(t, s.startAPIServerInformer())
	require.Zero(t, s.queue.Len())
	require.NoError(t, client.CoreV1().Pods(pod.Namespace).Delete(
		context.Background(),
		pod.Name,
		metav1.DeleteOptions{},
	))

	require.Eventually(t, func() bool { return s.queue.Len() == 1 }, time.Second, 10*time.Millisecond)
}

func TestBrokerInformerRegistersDeleteHandler(t *testing.T) {
	daemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name:      "topograph-node-data-broker",
		Namespace: "topograph",
	}}
	client := k8sfake.NewSimpleClientset(daemonSet)
	s, err := NewStatusInformer(
		context.Background(),
		client,
		nil,
		nil,
		daemonSet.Name,
		daemonSet.Namespace,
		0,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		s.Stop(nil)
	})

	require.NoError(t, s.startBrokerInformer())

	// The initial list delivers an add, so drain it before asserting on the delete.
	require.Eventually(t, func() bool { return s.queue.Len() == 1 }, time.Second, 10*time.Millisecond)
	key, shutdown := s.queue.Get()
	require.False(t, shutdown)
	s.queue.Done(key)
	s.queue.Forget(key)
	require.Zero(t, s.queue.Len())

	require.NoError(t, client.AppsV1().DaemonSets(daemonSet.Namespace).Delete(
		context.Background(),
		daemonSet.Name,
		metav1.DeleteOptions{},
	))

	require.Eventually(t, func() bool { return s.queue.Len() == 1 }, time.Second, 10*time.Millisecond)
}

func TestPodInformerRegistersDeleteHandler(t *testing.T) {
	// An unready pod keeps the initial add from queueing a request of its own.
	pod := makeWorkloadPod(false, makeContainerStatus("topograph", false, 0))
	client := k8sfake.NewSimpleClientset(pod)
	s, err := NewStatusInformer(
		context.Background(),
		client,
		&Trigger{PodSelector: &metav1.LabelSelector{}},
		nil,
		"",
		"",
		0,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		s.Stop(nil)
	})

	require.NoError(t, s.startPodInformer())
	require.Zero(t, s.queue.Len())

	require.NoError(t, client.CoreV1().Pods(pod.Namespace).Delete(
		context.Background(),
		pod.Name,
		metav1.DeleteOptions{},
	))

	require.Eventually(t, func() bool { return s.queue.Len() == 1 }, time.Second, 10*time.Millisecond)
}

func reqExecFunc(f httpreq.RequestFunc, _ bool) ([]byte, *httperr.Error) {
	if _, err := f(); err != nil {
		return nil, err
	}
	return nil, nil
}

func newTestStatusInformer(t *testing.T, retryDelay time.Duration, reqFunc httpreq.RequestFunc) *StatusInformer {
	t.Helper()
	s, err := NewStatusInformer(context.Background(), nil, nil, nil, "", "", retryDelay, reqFunc)
	require.NoError(t, err)
	s.reqExecFunc = reqExecFunc
	t.Cleanup(func() { s.Stop(nil) })
	return s
}

func TestSendRequestAndRetry(t *testing.T) {
	var calls int32

	// first two calls fails, third succeeds
	reqFunc := func() (*http.Request, *httperr.Error) {
		switch atomic.AddInt32(&calls, 1) {
		case 1, 2:
			return nil, httperr.NewError(http.StatusInternalServerError, "")
		default:
			return nil, nil
		}
	}

	s := newTestStatusInformer(t, 50*time.Millisecond, reqFunc)

	// start worker
	go s.run()

	// trigger request
	s.sendRequest()

	// wait enough time for: fail + delay + fail + delay + success
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) == 3
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		return s.queue.NumRequeues(topologyQueueKey) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestReconcileRequeuesWhileBrokerCacheIsNotReady(t *testing.T) {
	var calls int32
	s, err := NewStatusInformer(
		context.Background(),
		k8sfake.NewSimpleClientset(),
		nil,
		nil,
		"topograph-node-data-broker",
		"topograph",
		time.Second,
		func() (*http.Request, *httperr.Error) {
			atomic.AddInt32(&calls, 1)
			return nil, nil
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { s.Stop(nil) })

	requeueAfter, reconcileErr := s.reconcile()
	require.NoError(t, reconcileErr)
	require.Equal(t, defaultBrokerRetryDelay, requeueAfter)
	require.Zero(t, atomic.LoadInt32(&calls))
}

func TestDeduplicatesRequests(t *testing.T) {
	var calls int32

	reqFunc := func() (*http.Request, *httperr.Error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	}

	s := newTestStatusInformer(t, 50*time.Millisecond, reqFunc)

	// flood with requests
	for range 5 {
		s.sendRequest()
	}
	require.Equal(t, 1, s.queue.Len())
	go s.run()

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) == 1
	}, time.Second, 10*time.Millisecond)
}

func TestStartBlocksUntilStopped(t *testing.T) {
	s, err := NewStatusInformer(
		context.Background(),
		nil,
		nil,
		nil,
		"",
		"",
		0,
		nil,
	)
	require.NoError(t, err)
	done := make(chan error, 1)

	go func() {
		done <- s.Start()
	}()

	select {
	case err := <-done:
		require.Failf(t, "Start returned before Stop", "error: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	s.Stop(nil)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		require.Fail(t, "Start did not return after Stop")
	}
}

func TestStartDoesNotRequestWithoutInformerEvent(t *testing.T) {
	requestAttempted := make(chan struct{}, 1)
	s := newTestStatusInformer(t, time.Second, func() (*http.Request, *httperr.Error) {
		requestAttempted <- struct{}{}
		return nil, nil
	})
	done := make(chan error, 1)
	go func() {
		done <- s.Start()
	}()

	select {
	case <-requestAttempted:
		t.Fatal("Start issued a topology request without an informer event")
	case <-time.After(100 * time.Millisecond):
	}

	s.Stop(nil)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestWaitForInformerCacheReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForInformerCache(ctx, "nodes", func() bool { return false })
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "failed to sync nodes informer cache")
}

func TestStopCancelsRequestContext(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://topograph.invalid", nil)
	require.NoError(t, err)
	s, err := NewStatusInformer(
		context.Background(),
		nil,
		nil,
		nil,
		"",
		"",
		0,
		func() (*http.Request, *httperr.Error) {
			return req, nil
		},
	)
	require.NoError(t, err)

	boundReq, httpErr := s.reqFunc()
	require.Nil(t, httpErr)
	s.Stop(nil)

	select {
	case <-boundReq.Context().Done():
		require.ErrorIs(t, boundReq.Context().Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("request context was not cancelled by Stop")
	}
}

func TestStopCancelsRequestRetry(t *testing.T) {
	requestAttempted := make(chan struct{}, 1)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://topograph.invalid", nil)
	require.NoError(t, err)
	reqFunc := func() (*http.Request, *httperr.Error) {
		requestAttempted <- struct{}{}
		return req, httperr.NewError(http.StatusServiceUnavailable, "retry")
	}
	s, err := NewStatusInformer(context.Background(), nil, nil, nil, "", "", 0, reqFunc)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- s.Start()
	}()
	s.sendRequest()

	select {
	case <-requestAttempted:
	case <-time.After(time.Second):
		s.Stop(nil)
		t.Fatal("request was not attempted")
	}

	s.Stop(nil)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Stop cancelled the request retry")
	}
}

func TestNewRequestRunsBeforeRateLimitedRetry(t *testing.T) {
	var calls int32

	// always fail
	reqFunc := func() (*http.Request, *httperr.Error) {
		atomic.AddInt32(&calls, 1)
		return nil, httperr.NewError(http.StatusInternalServerError, "")
	}

	s := newTestStatusInformer(t, time.Second, reqFunc)

	go s.run()

	s.sendRequest()

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) == 1
	}, time.Second, 10*time.Millisecond)

	// send another request before retry fires
	s.sendRequest()

	// expected 2 calls:
	// - initial
	// - immediate second (not waiting full retryDelay)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) == 2
	}, 500*time.Millisecond, 10*time.Millisecond)
}

func TestSuccessfulEventSupersedesPendingRetry(t *testing.T) {
	const retryDelay = 100 * time.Millisecond
	var calls int32
	thirdCall := make(chan struct{}, 1)

	reqFunc := func() (*http.Request, *httperr.Error) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			return nil, httperr.NewError(http.StatusInternalServerError, "retry")
		}
		if call > 2 {
			select {
			case thirdCall <- struct{}{}:
			default:
			}
		}
		return nil, nil
	}

	s := newTestStatusInformer(t, retryDelay, reqFunc)
	go s.run()

	// The first request fails and schedules a delayed retry.
	s.sendRequest()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) == 1
	}, time.Second, 10*time.Millisecond)

	// A new event reconciles successfully before the delayed retry fires.
	s.sendRequest()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) == 2
	}, time.Second, 10*time.Millisecond)

	// The obsolete delayed item may still enter the queue, but it must not
	// issue another topology-generation request.
	select {
	case <-thirdCall:
		t.Fatal("obsolete delayed retry issued a third request")
	case <-time.After(3 * retryDelay):
	}
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestNewFailedEventResetsPendingRetry(t *testing.T) {
	var calls int32
	s := newTestStatusInformer(t, time.Second, func() (*http.Request, *httperr.Error) {
		atomic.AddInt32(&calls, 1)
		return nil, httperr.NewError(http.StatusInternalServerError, "retry")
	})

	// The initial event fails and schedules a retry.
	s.sendRequest()
	require.True(t, s.processNextWorkItem())
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// A newer event runs immediately, fails, and resets the retry deadline.
	s.sendRequest()
	require.True(t, s.processNextWorkItem())
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))

	// Simulate the older delayed entry reaching the queue before the reset
	// deadline. It must be postponed without issuing another request.
	s.queue.Add(topologyQueueKey)
	require.True(t, s.processNextWorkItem())
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func makeWorkloadPod(ready bool, statuses ...corev1.ContainerStatus) *corev1.Pod {
	conditionStatus := corev1.ConditionFalse
	if ready {
		conditionStatus = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "topograph-abc",
			Namespace: "topograph",
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: conditionStatus,
				},
			},
			ContainerStatuses: statuses,
		},
	}
}

func makeContainerStatus(name string, ready bool, restarts int32) corev1.ContainerStatus {
	status := corev1.ContainerStatus{
		Name:         name,
		Ready:        ready,
		RestartCount: restarts,
	}
	if ready {
		status.State.Running = &corev1.ContainerStateRunning{}
	}
	return status
}
