/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package node_observer

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/internal/k8s"
)

const topologyQueueKey = "cluster-topology"

// StatusInformer watches the configured Kubernetes resources and reconciles
// their cluster-wide topology through a single rate-limited work queue key.
// The exported name is retained for compatibility with existing callers.
type StatusInformer struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	nodeFactory         informers.SharedInformerFactory
	podFactory          informers.SharedInformerFactory
	apiFactory          informers.SharedInformerFactory
	brokerFactory       informers.SharedInformerFactory
	reqFunc             httpreq.RequestFunc
	reqExecFunc         func(httpreq.RequestFunc, bool) ([]byte, *httperr.Error)
	queue               workqueue.TypedRateLimitingInterface[string]
	retryDelay          time.Duration
	stopOnce            sync.Once
	requestedGeneration atomic.Uint64
	completedGeneration atomic.Uint64
	attemptedGeneration uint64
	retryNotBefore      time.Time

	apiServerContainerName string
}

func NewStatusInformer(ctx context.Context, client kubernetes.Interface, trigger *Trigger, apiServer *APIServer, brokerName, brokerNamespace string, retryDelay time.Duration, reqFunc httpreq.RequestFunc) (*StatusInformer, error) {
	klog.InfoS("Configuring status informer", "trigger", trigger, "apiServer", apiServer, "brokerName", brokerName, "brokerNamespace", brokerNamespace)
	if retryDelay <= 0 {
		retryDelay = defaultRetryDelay
	}

	statusInformer := &StatusInformer{
		reqFunc:     reqFunc,
		reqExecFunc: httpreq.DoRequestWithRetries,
		retryDelay:  retryDelay,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](retryDelay, retryDelay),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "node-observer"},
		),
	}

	if trigger != nil && len(trigger.NodeSelector) != 0 {
		listOptionsFunc := func(options *metav1.ListOptions) {
			options.LabelSelector = labels.Set(trigger.NodeSelector).AsSelector().String()
		}
		statusInformer.nodeFactory = informers.NewSharedInformerFactoryWithOptions(
			client, 0, informers.WithTweakListOptions(listOptionsFunc))
	}

	if trigger != nil && trigger.PodSelector != nil {
		podFactory, err := newPodFactory(client, trigger.PodSelector, "")
		if err != nil {
			return nil, err
		}
		statusInformer.podFactory = podFactory
	}

	if apiServer != nil && apiServer.PodSelector != nil {
		podFactory, err := newPodFactory(client, apiServer.PodSelector, apiServer.Namespace)
		if err != nil {
			return nil, err
		}
		statusInformer.apiFactory = podFactory
		statusInformer.apiServerContainerName = apiServer.ContainerName
	}

	if brokerName != "" && brokerNamespace != "" {
		listOptionsFunc := func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", brokerName).String()
		}
		statusInformer.brokerFactory = informers.NewSharedInformerFactoryWithOptions(
			client,
			0,
			informers.WithNamespace(brokerNamespace),
			informers.WithTweakListOptions(listOptionsFunc),
		)
	}

	statusInformer.ctx, statusInformer.cancel = context.WithCancel(ctx)
	statusInformer.reqFunc = requestFuncWithContext(statusInformer.ctx, reqFunc)
	return statusInformer, nil
}

func requestFuncWithContext(ctx context.Context, f httpreq.RequestFunc) httpreq.RequestFunc {
	if f == nil {
		return nil
	}
	return func() (*http.Request, *httperr.Error) {
		req, err := f()
		if req != nil {
			req = req.WithContext(ctx)
		}
		return req, err
	}
}

func newPodFactory(client kubernetes.Interface, selector *metav1.LabelSelector, namespace string) (informers.SharedInformerFactory, error) {
	s, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, err
	}

	listOptionsFunc := func(options *metav1.ListOptions) {
		options.LabelSelector = s.String()
	}

	options := []informers.SharedInformerOption{
		informers.WithTweakListOptions(listOptionsFunc),
	}
	if namespace != "" {
		options = append(options, informers.WithNamespace(namespace))
	}

	return informers.NewSharedInformerFactoryWithOptions(client, 0, options...), nil
}

func (s *StatusInformer) Start() error {
	klog.Info("Starting status informer")

	if err := s.startNodeInformer(); err != nil {
		return err
	}

	if err := s.startPodInformer(); err != nil {
		return err
	}

	if err := s.startAPIServerInformer(); err != nil {
		return err
	}

	if err := s.startBrokerInformer(); err != nil {
		return err
	}

	s.run()
	return nil
}

func (s *StatusInformer) Stop(_ error) {
	s.stopOnce.Do(func() {
		klog.Info("Stopping status informer")
		s.cancel()
		s.queue.ShutDown()
		if s.nodeFactory != nil {
			s.nodeFactory.Shutdown()
		}
		if s.podFactory != nil {
			s.podFactory.Shutdown()
		}
		if s.apiFactory != nil {
			s.apiFactory.Shutdown()
		}
		if s.brokerFactory != nil {
			s.brokerFactory.Shutdown()
		}
	})
}

// requestOnDelete returns a delete handler for the named resource. A relist
// tombstone can carry a nil object, so the request must not depend on it.
func (s *StatusInformer) requestOnDelete(kind string) func(any) {
	return func(obj any) {
		if key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err != nil {
			klog.V(4).Infof("Informer deleted %s with unidentifiable object: %v", kind, err)
		} else {
			klog.V(4).Infof("Informer deleted %s %s", kind, key)
		}
		s.sendRequest()
	}
}

func (s *StatusInformer) startNodeInformer() error {
	if s.nodeFactory != nil {
		informer := s.nodeFactory.Core().V1().Nodes().Informer()
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if node, ok := obj.(*corev1.Node); ok {
					klog.V(4).Infof("Informer added node %s", node.Name)
					s.sendRequest()
				}
			},
			DeleteFunc: s.requestOnDelete("node"),
		})
		if err != nil {
			return err
		}
		s.nodeFactory.Start(s.ctx.Done())
		if err := waitForInformerCache(s.ctx, "nodes", informer.HasSynced); err != nil {
			return err
		}
	}
	return nil
}

func (s *StatusInformer) startAPIServerInformer() error {
	if s.apiFactory != nil {
		informer := s.apiFactory.Core().V1().Pods().Informer()
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				pod, ok := obj.(*corev1.Pod)
				if !ok {
					return
				}
				if isAPIServerPodReady(pod, s.apiServerContainerName) {
					klog.V(4).Infof("Informer added ready API server pod %s/%s", pod.Namespace, pod.Name)
					s.sendRequest()
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				oldPod, ok := oldObj.(*corev1.Pod)
				if !ok {
					return
				}
				newPod, ok := newObj.(*corev1.Pod)
				if !ok {
					return
				}
				if shouldRequestOnAPIServerUpdate(oldPod, newPod, s.apiServerContainerName) {
					klog.V(4).Infof("Informer updated ready API server pod %s/%s", newPod.Namespace, newPod.Name)
					s.sendRequest()
				}
			},
			DeleteFunc: s.requestOnDelete("API server pod"),
		})
		if err != nil {
			return err
		}
		s.apiFactory.Start(s.ctx.Done())
		if err := waitForInformerCache(s.ctx, "API server pods", informer.HasSynced); err != nil {
			return err
		}
	}
	return nil
}

func (s *StatusInformer) startBrokerInformer() error {
	if s.brokerFactory != nil {
		informer := s.brokerFactory.Apps().V1().DaemonSets().Informer()
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				daemonSet, ok := obj.(*appsv1.DaemonSet)
				if !ok {
					return
				}
				klog.V(4).Infof("Informer added node-data-broker DaemonSet %s/%s", daemonSet.Namespace, daemonSet.Name)
				s.sendRequest()
			},
			UpdateFunc: func(oldObj, newObj any) {
				oldDaemonSet, ok := oldObj.(*appsv1.DaemonSet)
				if !ok {
					return
				}
				newDaemonSet, ok := newObj.(*appsv1.DaemonSet)
				if !ok {
					return
				}
				if isBrokerDaemonSetReady(oldDaemonSet) != isBrokerDaemonSetReady(newDaemonSet) ||
					oldDaemonSet.Status.DesiredNumberScheduled != newDaemonSet.Status.DesiredNumberScheduled {
					klog.V(4).Infof("Informer updated node-data-broker DaemonSet %s/%s", newDaemonSet.Namespace, newDaemonSet.Name)
					s.sendRequest()
				}
			},
			DeleteFunc: s.requestOnDelete("node-data-broker DaemonSet"),
		})
		if err != nil {
			return err
		}
		s.brokerFactory.Start(s.ctx.Done())
		if err := waitForInformerCache(s.ctx, "node-data-broker DaemonSet", informer.HasSynced); err != nil {
			return err
		}
	}
	return nil
}

func (s *StatusInformer) startPodInformer() error {
	if s.podFactory != nil {
		informer := s.podFactory.Core().V1().Pods().Informer()
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if pod, ok := obj.(*corev1.Pod); ok {
					if k8s.IsPodReady(pod) {
						klog.V(4).Infof("Informer added pod %s/%s", pod.Namespace, pod.Name)
						s.sendRequest()
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				oldPod, ok := oldObj.(*corev1.Pod)
				if !ok {
					return
				}
				newPod, ok := newObj.(*corev1.Pod)
				if !ok {
					return
				}
				if k8s.IsPodReady(oldPod) != k8s.IsPodReady(newPod) {
					klog.V(4).Infof("Informer updated pod %s/%s", newPod.Namespace, newPod.Name)
					s.sendRequest()
				}
			},
			DeleteFunc: s.requestOnDelete("pod"),
		})
		if err != nil {
			return err
		}
		s.podFactory.Start(s.ctx.Done())
		if err := waitForInformerCache(s.ctx, "trigger pods", informer.HasSynced); err != nil {
			return err
		}
	}
	return nil
}

func waitForInformerCache(ctx context.Context, name string, synced cache.InformerSynced) error {
	if cache.WaitForCacheSync(ctx.Done(), synced) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("failed to sync %s informer cache: %w", name, err)
	}
	return fmt.Errorf("failed to sync %s informer cache", name)
}

func (s *StatusInformer) sendRequest() {
	s.requestedGeneration.Add(1)
	s.queue.Add(topologyQueueKey)
}

func (s *StatusInformer) run() {
	for s.processNextWorkItem() {
	}
}

func (s *StatusInformer) processNextWorkItem() bool {
	key, shutdown := s.queue.Get()
	if shutdown {
		return false
	}
	defer s.queue.Done(key)

	// Queue entries carry only the cluster key; generations determine their meaning:
	//   - completed generation: obsolete delayed entry, discard
	//   - newer than attempted: new informer event, reconcile immediately
	//   - already attempted before retryNotBefore: early delayed entry, postpone
	//   - otherwise: retry deadline reached, reconcile
	generation := s.requestedGeneration.Load()
	if generation <= s.completedGeneration.Load() {
		// AddAfter entries cannot be cancelled. If a newer informer event has
		// already reconciled successfully, discard the obsolete delayed item.
		s.queue.Forget(key)
		return true
	}
	if generation == s.attemptedGeneration && time.Now().Before(s.retryNotBefore) {
		// A newer informer event resets main's retry timer. A delayed workqueue
		// entry cannot be cancelled, so postpone an obsolete entry until the
		// latest attempt's retry deadline instead of reconciling too early.
		s.queue.AddAfter(key, time.Until(s.retryNotBefore))
		return true
	}

	requeueAfter, err := s.reconcile()
	if s.ctx.Err() != nil {
		s.queue.Forget(key)
		return false
	}
	if err != nil {
		klog.Errorf("Topology reconciliation failed: %v", err)
		s.attemptedGeneration = generation
		s.retryNotBefore = time.Now().Add(s.retryDelay)
		s.queue.AddRateLimited(key)
		return true
	}

	s.queue.Forget(key)
	if requeueAfter > 0 {
		s.attemptedGeneration = generation
		s.retryNotBefore = time.Now().Add(requeueAfter)
		s.queue.AddAfter(key, requeueAfter)
		return true
	}
	s.attemptedGeneration = generation
	s.retryNotBefore = time.Time{}
	s.completedGeneration.Store(generation)
	return true
}

func shouldRequestOnAPIServerUpdate(oldPod, newPod *corev1.Pod, containerName string) bool {
	return shouldRequestOnWorkloadUpdate(oldPod, newPod, containerName)
}

func shouldRequestOnWorkloadUpdate(oldPod, newPod *corev1.Pod, containerName string) bool {
	if !isWorkloadPodReady(newPod, containerName) {
		return false
	}
	if !isWorkloadPodReady(oldPod, containerName) {
		return true
	}

	oldRestarts, oldFound := containerRestartCount(oldPod, containerName)
	newRestarts, newFound := containerRestartCount(newPod, containerName)
	return oldFound && newFound && newRestarts > oldRestarts
}

func isAPIServerPodReady(pod *corev1.Pod, containerName string) bool {
	return isWorkloadPodReady(pod, containerName)
}

func isWorkloadPodReady(pod *corev1.Pod, containerName string) bool {
	return k8s.IsPodReady(pod) && isContainerRunningAndReady(pod, containerName)
}

func isContainerRunningAndReady(pod *corev1.Pod, containerName string) bool {
	found := false
	for _, status := range pod.Status.ContainerStatuses {
		if containerName != "" && status.Name != containerName {
			continue
		}
		found = true
		if status.Ready && status.State.Running != nil {
			return true
		}
	}
	return containerName == "" && !found
}

func containerRestartCount(pod *corev1.Pod, containerName string) (int32, bool) {
	if containerName != "" {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == containerName {
				return status.RestartCount, true
			}
		}
		return 0, false
	}

	var restarts int32
	found := false
	for _, status := range pod.Status.ContainerStatuses {
		restarts += status.RestartCount
		found = true
	}
	return restarts, found
}

func (s *StatusInformer) brokerReady() (bool, error) {
	if s.brokerFactory == nil {
		return true, nil
	}

	informer := s.brokerFactory.Apps().V1().DaemonSets().Informer()
	if !informer.HasSynced() {
		return false, nil
	}

	items := informer.GetStore().List()
	if len(items) != 1 {
		return false, nil
	}

	daemonSet, ok := items[0].(*appsv1.DaemonSet)
	if !ok {
		return false, nil
	}
	return brokerDaemonSetReady(daemonSet)
}

func isBrokerDaemonSetReady(daemonSet *appsv1.DaemonSet) bool {
	ready, _ := brokerDaemonSetReady(daemonSet)
	return ready
}

func brokerDaemonSetReady(daemonSet *appsv1.DaemonSet) (bool, error) {
	if daemonSet.Status.DesiredNumberScheduled == 0 {
		return false, fmt.Errorf(
			"node-data-broker DaemonSet %s/%s has 0 desired replicas; check its node selector, affinity, and tolerations",
			daemonSet.Namespace,
			daemonSet.Name,
		)
	}
	return daemonSet.Status.DesiredNumberScheduled == daemonSet.Status.NumberReady, nil
}

func (s *StatusInformer) reconcile() (time.Duration, error) {
	brokerReady, err := s.brokerReady()
	if err != nil {
		// Broker errors describe the current unready state. Log the diagnostic and
		// continue polling on the broker cadence rather than treating it as a
		// topology-generation failure.
		klog.Error(err)
	}
	if !brokerReady {
		klog.V(2).Info("Waiting for the node-data-broker DaemonSet to become ready before topology generation")
		return defaultBrokerRetryDelay, nil
	}

	if s.reqFunc == nil {
		return 0, nil
	}
	if _, err := s.reqExecFunc(s.reqFunc, false); err != nil {
		if s.ctx.Err() != nil {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to send topology generation request: %w", err)
	}
	return 0, nil
}
