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

package slinky

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

const (
	NAME = "slinky"

	ConfigUpdateModeNone         = "none"
	ConfigUpdateModeSkeletonOnly = "skeleton-only"

	dynamicShowPartitionNodes = "\tNodes=NONE"

	// Slinky component labels used to locate a pod that can run
	// `scontrol show partition`. The controller is always present; login pods
	// are optional.
	slurmComponentLabelKey   = "app.kubernetes.io/component"
	slurmComponentController = "controller"
	slurmComponentLogin      = "login"
)

// execInPod indirects k8s.ExecInPod so tests can exercise the partition
// discovery fallback logic without a real exec transport.
var execInPod = k8s.ExecInPod

type SlinkyEngine struct {
	config             *rest.Config
	client             kubernetes.Interface
	params             *Params
	cachedClusterNodes *clusterNodes
}

type clusterNodes struct {
	nodes   *corev1.NodeList
	nodeMap map[string]string
}

type Params struct {
	slurm.BaseParams `mapstructure:",squash"`
	// Namespace specifies the namespace where Slinky cluster is deployed
	Namespace string `mapstructure:"namespace"`
	// PodSelector specifies slurmd pods
	PodSelector metav1.LabelSelector `mapstructure:"podSelector"`
	// NodeSelector (optional) specifies nodes running slurmd pods
	NodeSelector map[string]string `mapstructure:"nodeSelector"`
	// ConfigMapName specifies the name of the configmap containing topology config
	ConfigMapName string `mapstructure:"topologyConfigmapName"`
	// ConfigPath specifies the topology config filename inside the configmap
	ConfigPath string `mapstructure:"topologyConfigPath"`
	// UseDynamicNodes specifies whether to use dynamic nodes for reporting: true or false
	UseDynamicNodes bool `mapstructure:"useDynamicNodes" default:"false"`
	// AcceleratorDomainSourceLabel optionally selects an existing Kubernetes
	// Node label as the authoritative block-domain source.
	AcceleratorDomainSourceLabel string `mapstructure:"acceleratorDomainSourceLabel"`
	// ConfigUpdateMode specifies the mode for updating the slurm config: valid values {"none", "skeleton-only"}
	ConfigUpdateMode string `mapstructure:"configUpdateMode,omitempty"`
	// Topologies specifies per-partition topology configuration
	Topologies map[string]*Topology `mapstructure:"topologies,omitempty"`

	// derived fields
	podListOpt  *metav1.ListOptions
	nodeListOpt *metav1.ListOptions
}

// Topology is the slinky-specific per-partition topology config.
// It extends slurm.Topology with PodSelector, which selects the pods whose
// hosts make up the partition. Exactly one of Nodes or PodSelector may be set;
// if neither is set, the engine falls back to "scontrol show partition".
type Topology struct {
	slurm.Topology `mapstructure:",squash"`
	// PodSelector selects the slurmd pods belonging to this partition.
	PodSelector metav1.LabelSelector `mapstructure:"podSelector"`
}

func NamedLoader() (string, engines.Loader) {
	return NAME, Loader
}

func Loader(_ context.Context, params engines.Config) (engines.Engine, *httperr.Error) {
	p, err := getParameters(params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	if err := k8s.ConfigureClientRateLimits(config); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return &SlinkyEngine{
		config: config,
		client: client,
		params: p,
	}, nil
}

func getParameters(params engines.Config) (*Params, error) {
	p := &Params{}
	if err := config.Decode(params, p); err != nil {
		return nil, err
	}
	if p.AcceleratorDomainSourceLabel != "" {
		if err := k8s.ValidateLabelKey("acceleratorDomainSourceLabel", p.AcceleratorDomainSourceLabel); err != nil {
			return nil, err
		}
	}
	// Validate config update mode
	if len(p.ConfigUpdateMode) != 0 && p.ConfigUpdateMode != ConfigUpdateModeNone && p.ConfigUpdateMode != ConfigUpdateModeSkeletonOnly {
		return nil, fmt.Errorf("invalid configUpdateMode: %s, must be either %s, or %s", p.ConfigUpdateMode, ConfigUpdateModeNone, ConfigUpdateModeSkeletonOnly)
	}
	sel, err := metav1.LabelSelectorAsSelector(&p.PodSelector)
	if err != nil {
		return nil, err
	}
	p.podListOpt = &metav1.ListOptions{
		LabelSelector: sel.String(),
	}

	if len(p.NodeSelector) != 0 {
		p.nodeListOpt = &metav1.ListOptions{
			LabelSelector: labels.Set(p.NodeSelector).String(),
		}
	}

	for key, val := range map[string]string{
		topology.KeyNamespace:         p.Namespace,
		topology.KeyPodSelector:       p.podListOpt.LabelSelector,
		topology.KeyTopoConfigPath:    p.ConfigPath,
		topology.KeyTopoConfigmapName: p.ConfigMapName,
	} {
		if len(val) == 0 {
			return nil, fmt.Errorf("must specify engine parameter %q", key)
		}
	}

	for name, t := range p.Topologies {
		if t == nil {
			return nil, fmt.Errorf("topology %q: nil entry", name)
		}
		if t.Nodes != nil && !isEmptySelector(&t.PodSelector) {
			return nil, fmt.Errorf("topology %q: cannot set both nodes and podSelector", name)
		}
	}

	return p, nil
}

func isEmptySelector(sel *metav1.LabelSelector) bool {
	return sel == nil || (len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0)
}

func (eng *SlinkyEngine) ResolveComputeInstances(ctx context.Context, instances []topology.ComputeInstances, _ any) ([]topology.ComputeInstances, *httperr.Error) {
	if len(instances) != 0 && !eng.params.UseDynamicNodes {
		return instances, nil
	}

	clusterNodes, err := eng.getClusterNodes(ctx)
	if err != nil {
		return nil, err
	}

	if len(instances) != 0 {
		return instances, nil
	}
	return getComputeInstances(clusterNodes.nodes, clusterNodes.nodeMap)
}

// resolveSlurmNodeName resolves a slurmd pod's SLURM node name, preferring the
// KeySlurmNodeName label and falling back to the pod's hostname.
func resolveSlurmNodeName(pod *corev1.Pod) string {
	if name, ok := pod.Labels[topology.KeySlurmNodeName]; ok {
		return name
	}
	return pod.Spec.Hostname
}

// getClusterNodes returns the Kubernetes nodes selected for topology generation
// and a map from Kubernetes node name to Slurm node name. The mapping is built
// from Ready slurmd pods in the configured namespace and pod selector, using the
// slurm.node.name label when present and falling back to pod.spec.hostname.
func (eng *SlinkyEngine) getClusterNodes(ctx context.Context) (*clusterNodes, *httperr.Error) {
	if eng.cachedClusterNodes != nil {
		return eng.cachedClusterNodes, nil
	}

	nodes, err := k8s.GetNodes(ctx, eng.client, eng.params.nodeListOpt)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	pods, err := eng.client.CoreV1().Pods(eng.params.Namespace).List(ctx, *eng.params.podListOpt)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway,
			fmt.Sprintf("failed to list SLURM pods in the cluster: %v", err))
	}

	klog.V(4).Infof("Found %d pods in %q namespace with selector %q", len(pods.Items), eng.params.Namespace, eng.params.podListOpt.LabelSelector)

	// map k8s host name to SLURM host name
	nodeMap := make(map[string]string)
	for _, pod := range pods.Items {
		if !k8s.IsPodReady(&pod) {
			continue
		}
		host := resolveSlurmNodeName(&pod)
		if host == "" {
			klog.Warningf("Cannot resolve SLURM node name for pod %s/%s, skipping", pod.Namespace, pod.Name)
			continue
		}
		klog.V(4).Infof("Mapping k8s node %s to SLURM node %s", pod.Spec.NodeName, host)
		nodeMap[pod.Spec.NodeName] = host
	}
	eng.cachedClusterNodes = &clusterNodes{
		nodes:   nodes,
		nodeMap: nodeMap,
	}
	return eng.cachedClusterNodes, nil
}

func getComputeInstances(nodes *corev1.NodeList, nodeMap map[string]string) ([]topology.ComputeInstances, *httperr.Error) {
	regions := make(map[string]map[string]string)
	regionNames := []string{}
	for _, node := range nodes.Items {
		hostName, ok := nodeMap[node.Name]
		if !ok {
			klog.V(4).Infof(
				"Cannot resolve k8s node %q: no resolvable mapping from a Ready SLURM pod; check the engine namespace, podSelector, pod readiness, and Slurm node-name metadata",
				node.Name,
			)
			continue
		}
		instance, ok := node.Annotations[topology.KeyNodeInstance]
		if !ok {
			klog.Warningf("missing %q annotation in node %s", topology.KeyNodeInstance, node.Name)
			continue
		}
		region, ok := node.Annotations[topology.KeyNodeRegion]
		if !ok {
			klog.Warningf("missing %q annotation in node %s", topology.KeyNodeRegion, node.Name)
			continue
		}
		klog.V(4).InfoS("Adding compute instance", "host", hostName, "node", node.Name, "instance", instance, "region", region)
		if _, ok = regions[region]; !ok {
			regions[region] = make(map[string]string)
			regionNames = append(regionNames, region)
		}
		regions[region][instance] = hostName
	}

	cis := make([]topology.ComputeInstances, 0, len(regions))
	for _, region := range regionNames {
		cis = append(cis, topology.ComputeInstances{Region: region, Instances: regions[region]})
	}

	return cis, nil
}

func withLabelBackedDomains(graph *topology.Graph, clusterNodes *clusterNodes, sourceLabel string) (*topology.Graph, *httperr.Error) {
	// No nodes selected at all is a distinct failure from "nodes exist but none
	// matched": it usually points at a too-narrow engine nodeSelector.
	if len(clusterNodes.nodes.Items) == 0 {
		return nil, httperr.NewError(http.StatusBadGateway,
			"no selected Kubernetes nodes found; check engine nodeSelector")
	}

	domains := topology.NewDomainMap()

	// Diagnostic counters to explain why no domains were built. The instance
	// annotation is written per-node by the node-data-broker DaemonSet, so a
	// node with the source label but no annotation points at a broker that has
	// not (yet) annotated that specific node.
	totalNodes := len(clusterNodes.nodes.Items)
	var noSlurmName, noSourceLabel int
	missingAnnotation := []string{}

	for _, node := range clusterNodes.nodes.Items {
		slurmName, ok := clusterNodes.nodeMap[node.Name]
		if !ok || slurmName == "" {
			noSlurmName++
			klog.V(4).Infof("Skipping node %s as it does not have a corresponding SLURM name", node.Name)
			continue
		}

		acceleratorDomain := strings.TrimSpace(node.Labels[sourceLabel])
		if acceleratorDomain == "" {
			noSourceLabel++
			klog.V(4).Infof("Skipping node %s (SLURM node %s): missing/empty %q label", node.Name, slurmName, sourceLabel)
			continue
		}

		instance := strings.TrimSpace(node.Annotations[topology.KeyNodeInstance])
		if instance == "" {
			missingAnnotation = append(missingAnnotation, node.Name)
			klog.Warningf("node %s (SLURM node %s) has label %s=%q but has a missing/empty annotation %q (expected from node-data-broker on that node)",
				node.Name, slurmName, sourceLabel, acceleratorDomain, topology.KeyNodeInstance)
			continue
		}

		domains.AddHost(acceleratorDomain, instance, slurmName)
	}

	if len(domains) == 0 {
		return nil, httperr.NewError(http.StatusBadGateway,
			fmt.Sprintf("acceleratorDomainSourceLabel=%q produced no usable label-backed domains; check label %q and annotation %q. "+
				"Scanned %d node(s): %d without a SLURM node mapping (no Ready slurmd pod), %d missing the %q label, %d with the label but a missing/empty %q annotation%s",
				sourceLabel, sourceLabel, topology.KeyNodeInstance,
				totalNodes, noSlurmName, noSourceLabel, sourceLabel,
				len(missingAnnotation), topology.KeyNodeInstance, formatMissingAnnotationNodes(missingAnnotation)))
	}

	if graph == nil {
		graph = &topology.Graph{Domains: domains}
	} else {
		cloned := *graph
		cloned.Domains = domains
		graph = &cloned
	}

	return graph, nil
}

// formatMissingAnnotationNodes renders the node names that carry the configured
// source label but lack the node-data-broker-written instance annotation. The list is
// capped so the error message stays bounded on large clusters.
func formatMissingAnnotationNodes(nodes []string) string {
	if len(nodes) == 0 {
		return ""
	}

	const maxListed = 10
	if len(nodes) <= maxListed {
		return fmt.Sprintf(" (nodes missing annotation: %s)", strings.Join(nodes, ", "))
	}

	return fmt.Sprintf(" (nodes missing annotation: %s, and %d more)",
		strings.Join(nodes[:maxListed], ", "), len(nodes)-maxListed)
}

func usesBlockTopology(cfg *translate.Config) bool {
	if cfg == nil {
		return false
	}

	if cfg.Plugin == topology.TopologyBlock {
		return true
	}

	for _, spec := range cfg.Topologies {
		if spec != nil && spec.Plugin == topology.TopologyBlock {
			return true
		}
	}

	return false
}

// generateConfigMapAnnotations creates metadata annotations for ConfigMaps
func (eng *SlinkyEngine) generateConfigMapAnnotations() map[string]string {
	annotations := map[string]string{
		topology.KeyConfigMapEngine:            NAME,
		topology.KeyConfigMapTopologyManagedBy: "topograph",
		topology.KeyConfigMapLastUpdated:       time.Now().Format(time.RFC3339),
		topology.KeyConfigMapNamespace:         eng.params.Namespace,
	}

	// Add plugin-specific annotations if available
	if len(eng.params.Plugin) != 0 {
		annotations[topology.KeyConfigMapPlugin] = eng.params.Plugin
	}
	if len(eng.params.BlockSizes) != 0 {
		annotations[topology.KeyConfigMapBlockSizes] = intToStr(eng.params.BlockSizes)
	}

	return annotations
}

func (eng *SlinkyEngine) GenerateOutput(ctx context.Context, graph *topology.Graph, _ map[string]any) ([]byte, *httperr.Error) {
	p := eng.params

	resolvedTopologies, err := eng.resolveTopologies(ctx)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	topologyNodeFinder := &slurm.TopologyNodeFinder{
		GetPartitionNodes: eng.getPartitionNodes,
		Params:            []any{p.Namespace},
	}
	cfg, httpErr := slurm.GetTranslateConfig(ctx, &p.BaseParams, resolvedTopologies, topologyNodeFinder)
	if httpErr != nil {
		return nil, httpErr
	}

	var clusterNodeData *clusterNodes
	loadClusterNodes := func() (*clusterNodes, *httperr.Error) {
		if clusterNodeData != nil {
			return clusterNodeData, nil
		}
		var httpErr *httperr.Error
		clusterNodeData, httpErr = eng.getClusterNodes(ctx)
		return clusterNodeData, httpErr
	}

	if p.AcceleratorDomainSourceLabel != "" && usesBlockTopology(cfg) {
		clusterNodeData, httpErr := loadClusterNodes()
		if httpErr != nil {
			return nil, httpErr
		}
		graph, httpErr = withLabelBackedDomains(graph, clusterNodeData, p.AcceleratorDomainSourceLabel)
		if httpErr != nil {
			return nil, httpErr
		}
	}

	nt, err := translate.NewNetworkTopology(graph, cfg)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	// Get desired topology from root topology graph
	buf := &bytes.Buffer{}
	topologies, httpErr := nt.GenerateTopologyConfig(buf, p.ConfigUpdateMode == ConfigUpdateModeSkeletonOnly)
	if httpErr != nil {
		return nil, httpErr
	}
	desiredTopology := buf.String()

	// If the slurm config update mode is not none, update the slurm config
	if p.ConfigUpdateMode != ConfigUpdateModeNone {
		data := map[string]string{p.ConfigPath: desiredTopology}
		if err := eng.UpdateTopologyConfigmap(ctx, p.ConfigMapName, p.Namespace, data); err != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
		}
	}

	// For dynamic mode, perform reconciliation using the latest topology information from the provider (root) and the cluster (nodes and their annotations)
	if p.UseDynamicNodes {
		clusterNodeData, httpErr := loadClusterNodes()
		if httpErr != nil {
			return nil, httpErr
		}
		httpErr = eng.performReconciliation(ctx, nt, topologies, clusterNodeData)
		if httpErr != nil {
			return nil, httpErr
		}
	}

	return []byte("OK\n"), nil
}

func (eng *SlinkyEngine) UpdateTopologyConfigmap(ctx context.Context, name, namespace string, data map[string]string) error {
	klog.Infof("Updating topology config %s/%s", namespace, name)
	klog.V(4).Infof("Updating topology config %s/%s with data: %v", namespace, name, data)

	annotations := eng.generateConfigMapAnnotations()
	verb := "get"
	cmClient := eng.client.CoreV1().ConfigMaps(namespace)
	cm, err := cmClient.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		verb = "update"

		changed := false
		for key, value := range data {
			if cm.Data[key] != value {
				changed = true
				break
			}
		}

		if !changed {
			klog.Infof("No changes to configmap %s/%s found, skipping update", namespace, name)
			return nil
		}

		// If the config map data is nil, create a new map
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		maps.Copy(cm.Data, data)

		// Apply annotations to existing ConfigMap
		if cm.Annotations == nil {
			cm.Annotations = make(map[string]string)
		}
		maps.Copy(cm.Annotations, annotations)

		_, err = cmClient.Update(ctx, cm, metav1.UpdateOptions{})
	} else if errors.IsNotFound(err) {
		verb = "create"
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Annotations: annotations,
			},
			Data: data,
		}
		_, err = cmClient.Create(ctx, cm, metav1.CreateOptions{})
	}

	if err != nil {
		return fmt.Errorf("failed to %s configmap %s/%s: %v",
			verb, namespace, name, err)
	}

	klog.Infof("Successfully %sd configmap %s/%s", verb, namespace, name)
	return nil
}

// resolveTopologies converts slinky.Topologies into slurm.Topology entries,
// resolving per-partition pod selectors into concrete node lists. Entries
// with explicit Nodes pass through; entries with neither Nodes nor PodSelector
// are left with empty Nodes so slurm.GetTranslateConfig invokes the scontrol
// fallback.
func (eng *SlinkyEngine) resolveTopologies(ctx context.Context) (map[string]*slurm.Topology, error) {
	if len(eng.params.Topologies) == 0 {
		return nil, nil
	}
	out := make(map[string]*slurm.Topology, len(eng.params.Topologies))
	for name, t := range eng.params.Topologies {
		st := t.Topology
		if len(st.Nodes) == 0 && !isEmptySelector(&t.PodSelector) {
			nodes, err := eng.listPartitionNodes(ctx, &t.PodSelector)
			if err != nil {
				return nil, fmt.Errorf("topology %q: %w", name, err)
			}
			st.Nodes = nodes
		}
		out[name] = &st
	}
	return out, nil
}

// listPartitionNodes lists pods in the engine's namespace matching sel and
// returns the corresponding SLURM node names from the KeySlurmNodeName label
// (falling back to pod.Spec.Hostname). Pods that are not Ready are skipped,
// mirroring the main pod-listing path.
func (eng *SlinkyEngine) listPartitionNodes(ctx context.Context, sel *metav1.LabelSelector) ([]string, error) {
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil, err
	}
	pods, err := eng.client.CoreV1().Pods(eng.params.Namespace).List(ctx, metav1.ListOptions{LabelSelector: s.String()})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with selector %q: %v", s.String(), err)
	}
	names := make([]string, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if !k8s.IsPodReady(&pod) {
			klog.Warningf("topology: skipped not Ready pod %s/%s (selector %s)", pod.Namespace, pod.Name, s.String())
			continue
		}
		host := resolveSlurmNodeName(&pod)
		if host == "" {
			klog.Warningf("topology: cannot find Slurm hostname for pod %s/%s (selector %s)", pod.Namespace, pod.Name, s.String())
			continue
		}
		names = append(names, host)
	}
	return names, nil
}

func (eng *SlinkyEngine) getPartitionNodes(ctx context.Context, partition string, params []any) (string, error) {
	if eng.params.UseDynamicNodes {
		klog.Infof("Skipping - scontrol show partition - when using useDynamicNodes flag")
		return dynamicShowPartitionNodes, nil
	}

	if len(params) != 1 {
		return "", fmt.Errorf("getPartitionNodes expects a namespace as a parameter")
	}
	namespace, ok := params[0].(string)
	if !ok {
		return "", fmt.Errorf("getPartitionNodes expects a string parameter")
	}

	// A Slinky cluster always runs a controller (slurmctld) but login pods are
	// optional, so prefer the controller for `scontrol show partition` and fall
	// back to a login pod when one is present. Both listing and exec failures
	// for a component are non-fatal: they are recorded and the next component is
	// tried, so an RBAC/NetworkPolicy rule scoped to only one component (e.g.
	// pods/exec allowed on login but not controller) cannot break discovery that
	// worked before the controller leg was added.
	var discoveryErrs []string
	for _, component := range []string{slurmComponentController, slurmComponentLogin} {
		labels := map[string]string{slurmComponentLabelKey: component}
		pods, err := k8s.GetPodsByLabels(ctx, eng.client, namespace, labels)
		if err != nil {
			klog.Warningf("topology: failed to list %s pods in namespace %q: %v; trying next component", component, namespace, err)
			discoveryErrs = append(discoveryErrs, fmt.Sprintf("list %s pods: %v", component, err))
			continue
		}

		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			cmd := []string{"scontrol", "show", "partition", partition}
			buf, err := execInPod(ctx, eng.client, eng.config, pod.Name, pod.Namespace, cmd)
			if err != nil {
				klog.Warningf("topology: scontrol exec failed on %s pod %s/%s: %v; trying next component", component, pod.Namespace, pod.Name, err)
				discoveryErrs = append(discoveryErrs, fmt.Sprintf("exec on %s pod %s: %v", component, pod.Name, err))
				continue
			}

			return buf.String(), nil
		}
	}

	if len(discoveryErrs) > 0 {
		return "", fmt.Errorf("partition discovery failed on all %s and %s pods in namespace %q: %s",
			slurmComponentController, slurmComponentLogin, namespace, strings.Join(discoveryErrs, "; "))
	}

	return "", fmt.Errorf("no running %s or %s pods found for partition discovery in namespace %q",
		slurmComponentController, slurmComponentLogin, namespace)
}

func (eng *SlinkyEngine) performReconciliation(ctx context.Context, nt *translate.NetworkTopology, topologies []*translate.TopologyUnit, clusterNodes *clusterNodes) *httperr.Error {
	// Update node annotations based on the desired topology and the current cluster state.
	// This will trigger Slinky to reconfigure the nodes accordingly.
	for i := range clusterNodes.nodes.Items {
		node := &clusterNodes.nodes.Items[i]
		slurmName, ok := clusterNodes.nodeMap[node.Name]
		if !ok {
			klog.V(4).Infof("Skipping node %s as it does not have a corresponding SLURM name", node.Name)
			continue
		}

		if httpErr := eng.updateNodeAnnotation(ctx, node, slurmName, nt, topologies); httpErr != nil {
			return httpErr
		}
		klog.V(4).Infof("Successfully updated annotation for node %s (SLURM name: %s)", node.Name, slurmName)

	}

	// Removed nodes: do nothing

	return nil
}

func (eng *SlinkyEngine) updateNodeAnnotation(ctx context.Context, node *corev1.Node, slurmName string, nt *translate.NetworkTopology, topologies []*translate.TopologyUnit) *httperr.Error {
	desiredSpec, httpErr := nt.GetNodeTopologySpec(slurmName, topologies)
	if httpErr != nil {
		return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to get topology spec for node %s: %v", slurmName, httpErr))
	}

	if desiredSpec == "" {
		klog.V(4).Infof("Node %s (SLURM name: %s) received no topology spec from the provider, skipping annotation update", node.Name, slurmName)
		return nil
	}

	// Use the annotation value from the already-fetched node (from the cluster List) to
	// avoid an extra Get per node. If the annotation is already correct, skip the Patch.
	if node.Annotations[topology.KeySlinkyTopologySpec] == desiredSpec {
		klog.V(4).Infof("Node %s (SLURM name: %s) topology spec is up to date (spec = %s), skipping annotation update", node.Name, slurmName, desiredSpec)
		return nil
	}

	klog.Infof("Updating node %s (SLURM name: %s) topology spec annotation. Current spec: %q, New spec: %q",
		node.Name, slurmName, node.Annotations[topology.KeySlinkyTopologySpec], desiredSpec)

	patchData, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				topology.KeySlinkyTopologySpec: desiredSpec,
			},
		},
	})
	if err != nil {
		return httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to marshal patch for node %s: %v", node.Name, err))
	}

	_, err = eng.client.CoreV1().Nodes().Patch(ctx, node.Name, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
	if err != nil {
		return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to patch node annotation: %v", err))
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[topology.KeySlinkyTopologySpec] = desiredSpec

	return nil
}

func intToStr(input []int) string {
	strs := make([]string, len(input))
	for i, n := range input {
		strs[i] = strconv.Itoa(n)
	}
	return strings.Join(strs, ",")
}
