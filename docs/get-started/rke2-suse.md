# Install on RKE2 (SUSE)

Topograph installs on [RKE2](https://docs.rke2.io/), including RKE2 on SUSE Linux Enterprise Server (SLES) and openSUSE, with the standard Helm flow in [Install on Kubernetes](quickstart-k8s.md). Read that guide first. This page covers only what differs on RKE2.

Validated with chart `1.0.0` on:

- RKE2 v1.36.3 on SLES 16.0 (arm64) with NVIDIA GPU Operator v26.3.3, NVIDIA BlueField DPUs in RoCE mode, and the `infiniband-k8s` provider with `accelerator.source: nvidia-smi`
- RKE2 v1.36.4 on openSUSE Leap 16.0 (amd64), installed from RPM, with SELinux in enforcing mode

## GPU Operator location

With `accelerator.source: nvidia-smi`, the `infiniband-k8s` provider runs `nvidia-smi` inside the GPU Operator device plugin pod on each node. The chart's defaults, namespace `gpu-operator` and DaemonSet `nvidia-device-plugin-daemonset`, match a GPU Operator installed as described in the [RKE2 GPU Operator guide](https://docs.rke2.io/add-ons/gpu_operators). If you installed the operator into a different namespace, set `gpuOperatorNamespace`:

```yaml
provider:
  name: infiniband-k8s
  params:
    accelerator:
      source: nvidia-smi
      nvidiaSmi:
        gpuOperatorNamespace: <namespace>
```

In the default GPU Operator configuration from the RKE2 guide (without NRI), the device plugin pod runs with the `nvidia` RuntimeClass that RKE2 provides, so `nvidia-smi` works in it without extra configuration.

GPUs that are not part of a multi-node NVLink domain, such as PCIe cards, report `N/A` for their cluster UUID and clique ID. In that case the node-data-broker (the Topograph DaemonSet that collects per-node data) logs `No accelerator domain for node ...: ClusterUUID is N/A` and continues. This is expected, and the node still takes part in fabric discovery.

## Node names

This section applies to the InfiniBand providers.

RKE2 registers each node under its host name, which is often a fully qualified domain name (FQDN) such as `node-01.example.com`. An InfiniBand HCA usually reports the short host name in its node description, for example `node-01 mlx5_0`.

Topograph 1.0.0 and earlier match these names exactly, so on clusters with FQDN node names every node is dropped from the switch tree and no fabric labels are applied ([#563](https://github.com/dsx-ai-factory/topograph/issues/563)). Later releases fall back to matching by short host name, as described in the [InfiniBand provider](../providers/infiniband.md) documentation.

## Pod Security admission

RKE2 configures Pod Security admission for the whole cluster in `/etc/rancher/rke2/rke2-pss.yaml` (see [RKE2 Pod Security Standards](https://docs.rke2.io/security/pod_security_standards)). By default, RKE2 enforces the `privileged` level, and the chart installs without changes, including the privileged broker that the `infiniband-k8s` provider requires.

With the CIS profile (`profile: cis`), RKE2 enforces the `restricted` level in every namespace except `kube-system`, `compliance-operator-system` and `tigera-operator`. The API server and node-observer meet `restricted` with the chart's default security settings. The `infiniband-k8s` broker does not, because it needs `privileged: true` and a `hostPath` volume (see [Pod security context](../engines/k8s.md#pod-security-context)).

> **The install looks successful when it is not:** `helm install` reports `deployed`, but the broker DaemonSet never creates pods, and the node-observer waits for it indefinitely. No topology is generated.

To see the reason, list the DaemonSet's failed pod creations:

```bash
kubectl -n topograph get events --field-selector reason=FailedCreate
```

```text
Error creating: pods "topograph-node-data-broker-..." is forbidden: violates PodSecurity "restricted:latest":
privileged (container "node-data-broker" must not set securityContext.privileged=true), ...
restricted volume types (volume "sys-class-volume" uses restricted volume type "hostPath"), ...
```

To use InfiniBand discovery on a cluster with the CIS profile, create the Topograph namespace and set its enforcement level to `privileged` before you install the chart. A namespace label overrides the cluster default:

```bash
kubectl create namespace topograph
kubectl label namespace topograph pod-security.kubernetes.io/enforce=privileged
```

This lifts Pod Security restrictions for every pod in the namespace, so keep the namespace dedicated to Topograph.

Alternatively, exempt the namespace in a custom admission configuration file and point RKE2's `pod-security-admission-config-file` option at it. The custom file replaces the one RKE2 generates, so start from a copy of `/etc/rancher/rke2/rke2-pss.yaml` and add `topograph` to `exemptions.namespaces`.

## SELinux

SLES enables SELinux in enforcing mode by default. When RKE2 runs with [SELinux support](https://docs.rke2.io/security/selinux) enabled, Topograph needs no SELinux-specific configuration. An RPM install of RKE2 installs the `rke2-selinux` policy and enables this support automatically.

On the validation cluster:

- The API server and node-observer ran confined as `container_t`.
- The privileged broker ran as `spc_t`, the SELinux type for privileged containers, and read the host's `/sys/class` through its `hostPath` volume.
- No Topograph component triggered an SELinux denial.

## RoCE fabrics

`ibnetdiscover` works only on InfiniBand fabrics. On NICs in RoCE (Ethernet) mode it fails with `Can't open SMI UMAD port`, and the `infiniband-k8s` provider returns an empty switch tree. The request still completes, and Topograph leaves any existing topology labels on the nodes unchanged.

For NVIDIA Spectrum-X Ethernet fabrics, use the [NetQ provider](../providers/netq.md). Topograph does not currently discover other on-premises Ethernet fabrics.

## Verify

```bash
kubectl -n topograph get pods
kubectl -n topograph logs ds/topograph-node-data-broker | grep annotations
kubectl get nodes -o custom-columns='NAME:.metadata.name,INSTANCE:.metadata.annotations.topograph\.run/instance'
kubectl get nodes --show-labels | grep fabric.topograph.run
```

Each broker pod logs the `topograph.run/instance` and `topograph.run/region` annotations it applied, and every node should show an `INSTANCE` value. Fabric labels (`fabric.topograph.run/tier-N`) appear after the provider discovers a switch tree.
