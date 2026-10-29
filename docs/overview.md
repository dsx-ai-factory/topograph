# Topograph Overview

## What Topograph Is

Topograph is a component that **discovers the physical network topology of a cluster** and **exposes it to workload schedulers** (Slurm, Kubernetes, and Slurm-on-Kubernetes / Slinky) in the format each one expects.


## The Problem

At scale, workload placement becomes as important as resource allocation. Where a job runs can have a significant impact on its performance. For example, a distributed training job that spans nodes on opposite sides of a data center fabric incurs additional latency during every gradient synchronization. Similarly, a disaggregated inference pipeline that ignores NVLink locality may fail to utilize the full interconnect bandwidth available. In both cases, overlooking the underlying network topology leads to inefficient execution.

The physical network topology of a cluster plays a critical role in determining application performance. Modern GPU clusters are typically built on multi-tier network fabrics, where communication costs vary depending on node placement. Nodes connected to the same leaf switch can communicate with lower latency and higher bandwidth than nodes separated by multiple network hops. On advanced systems such as GB200/GB300 NVL72, groups of nodes share a high-speed NVLink fabric, forming a locality domain that significantly outperforms even the fastest Ethernet or InfiniBand connections.

Disaggregated inference serving illustrates the cost of ignoring topology. In these architectures, the prefill phase (processing the input prompt and producing the KV cache) and the decode phase (generating output tokens) run on separate, independently scalable GPU pools. At the boundary between them, the KV cache must transfer from prefill workers to decode workers — a large, latency-sensitive operation whose speed is entirely determined by the network path between the two pools. When prefill and decode workers are co-located within the same NVLink fabric the transfer is fast; when they land on opposite sides of a spine boundary and fall back to Ethernet, throughput drops and tail latency spikes.

To make optimal placement decisions, schedulers must be aware of this topology. However, topology information is often fragmented across multiple sources, including cloud provider APIs, fabric management systems, and low-level system tools. Each source exposes data through different interfaces and formats. At the same time, workload managers, such as Slurm, Kubernetes, or Slurm-on-Kubernetes deployments like Slinky, require this information in their own specific formats.

Topograph addresses this challenge. It discovers the physical network topology of a cluster and exposes it to schedulers in a form they can consume. By abstracting over diverse topology sources and translating them into the required output formats, Topograph transforms what would otherwise be a manual, environment-specific process into a unified and extensible pipeline.

## How It Works

Topograph operates around two primary concepts: a `provider` and an `engine`.

A provider represents a cloud or on-premises environment, while an engine refers to a scheduling system such as SLURM, Kubernetes, or hybrid SLURM-on-Kubernetes platforms such as Slinky.

This design allows Topograph to abstract across multiple topology sources — including CSP APIs, NVIDIA NetQ, and system-level tools — and translate that information into scheduler-specific outputs, such as Slurm topology.conf, Kubernetes node labels, or Slinky ConfigMaps.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/topograph-engine-outputs-community-dark.png" />
    <img src="assets/topograph-engine-outputs-community-light.png" width="100%" alt="Topograph engine output formats diagram" />
  </picture>
</p>

Topograph runs as a service and exposes several endpoints, which are described in detail in API.md.

Topology discovery is performed asynchronously. The /v1/generate endpoint first receives and validates a topology request, then returns a request ID. That request ID can be used with the /v1/topology endpoint to check the request status and retrieve the resulting topology data once the operation has completed successfully.

## Supported Environments

### Supported Providers

| Service provider or network fabric | Topograph provider name | Simulation variant |
|---|---|---|
| [Amazon Web Services (AWS)](./providers/aws.md) | `aws` | `aws-sim` |
| [Crusoe Cloud](./providers/crusoe.md) | `crusoe` | `crusoe-sim` |
| [Firebird](./providers/firebird.md) | `firebird` | `firebird-sim` |
| [Oracle Cloud Infrastructure (OCI)](./providers/oci.md) | `oci` (API), `oci-imds` (instance metadata) | `oci-sim` (API) |
| [Google Cloud Platform (GCP)](./providers/gcp.md) | `gcp` | `gcp-sim` |
| [Nebius](./providers/nebius.md) | `nebius` | `nebius-sim` |
| [Nscale](./providers/nscale.md) | `nscale` | `nscale-sim` |
| [Lambda](./providers/lambdai.md) | `lambdai` | `lambdai-sim` |
| [NVIDIA NetQ](./providers/netq.md) | `netq` | — |
| [Nautobot](./providers/nautobot.md) | `nautobot` | — |
| [NVIDIA NVLink partitions from Kubernetes Node labels](./providers/dra.md) | `dra` | — |
| [InfiniBand](./providers/infiniband.md) | `infiniband-bm` (bare metal), `infiniband-k8s` (Kubernetes) | [`infiniband-sim`](./providers/infiniband.md#infiniband-sim-offline-simulation) |
| [DSX topology model](./modeling.md#provider-simulations) | — | `dsx-sim` |
| [Topograph API lifecycle testing](./providers/test.md) | `test` | — (the provider itself simulates success, pending, and error responses) |

An em dash (—) indicates that no corresponding provider variant is registered.

Use [Nautobot](./providers/nautobot.md) when fabric cabling is inventoried in Nautobot; switch roles, locations, and platforms select the initial fabric switches, and compute roles identify their attached endpoints. Connected switches are followed beyond the location and platform filters. Switch names must be unique throughout the discovered fabric.

Currently supported engines:

- [SLURM](./engines/slurm.md)
- [Kubernetes](./engines/k8s.md)
- [Node Feature Discovery](./engines/nfd.md)
- [SLURM-on-Kubernetes (Slinky)](./engines/slinky.md)
- [Graph](./engines/graph.md) - returns per-instance network layers and accelerator labels as JSON

## Learn more

- [Architecture](./architecture.md)
- [Configuration and API](./api.md)
