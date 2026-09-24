# Lambda Topology Provider

The `lambdai` topology provider reads topology data from the Lambda topology API and converts it into Topograph's canonical three-tier topology graph.

The provider queries a single endpoint, `GET /api/v1/topology/instance`, which returns each instance's network switch path and (when available) its NVLink domain. From this it builds a switch tree (for Slurm `topology/tree` or Kubernetes labels) and, when NVLink data is present, an NVLink domain map (for `topology/block`).

## When to Use This Provider

Use this provider for Lambda Cloud clusters where the Lambda topology API is the topology source. It works with both the Slurm engine (generating `topology.conf`) and the Kubernetes engine (labeling nodes).

With the **Slurm engine**, `lambdai` does **not** auto-discover nodes: the topology request must supply explicit `nodes` — a per-region map of provider instance IDs to hostnames (see [Configuration](#configuration)). With the **Kubernetes engine**, node discovery is automatic via the node-data-broker (see [Kubernetes engine](#kubernetes-engine)). Either way, each region triggers one paginated API call.

## Prerequisites

- A Lambda topology API endpoint reachable from the Topograph host
- A Lambda workspace ID
- An API token with permission to read instance topology, or Kubernetes workload identity
- The region ID for each cluster you query

## Credentials

| Field | Required | Description |
|---|---|---|
| `workspaceId` | Yes\* | Lambda workspace ID; sent as the `workspace_id` query parameter |
| `token` | Yes\*\* | Bearer token used for topology API requests |

\* Required in both authentication modes, and always supplied as a credential. It is an identifier rather than a secret, so a workload-identity deployment does not need a Secret for it: set `provider.creds.workspaceId` in the Helm values and the Node Observer forwards it with each topology request.

\*\* Required for static-token authentication only. With [workload identity](#authentication-via-workload-identity) the API token is minted at runtime, so omit `token` entirely — a `token` supplied here always takes precedence over a workload identity, and an empty one is rejected rather than treated as a request to fall back.

Store credentials in a YAML file:

```yaml
workspaceId: <WORKSPACE_ID>
token: <API_TOKEN>
```

Reference that file from the Topograph config:

```yaml
credentialsPath: /etc/topograph/lambdai-credentials.yaml
```

Credentials can also be supplied directly in the topology request payload under `provider.creds`.

## Authentication via Workload Identity

Instead of storing a long-lived API token in a Kubernetes Secret, Topograph can authenticate with **Kubernetes workload identity**. On a Lambda Kubernetes Service (LKS) cluster, Lambda's `lambda-pod-identity-webhook` injects a projected ServiceAccount token into the API-server pod; Topograph exchanges that token at Lambda's OIDC endpoint (`POST /api/v1/oidc/token`) for a short-lived Lambda API key, which it uses for topology requests and refreshes automatically before expiry. No API token ever lives in the cluster.

The cluster's OIDC provider (issuer + JWKS) is **pre-registered by LKS** at provisioning, so there is no `audience` to manage and no provider to register from scratch. You only create an identity, grant it access, and trust the ServiceAccount subject.

### Prerequisites

- An **LKS cluster** — its OIDC issuer and public JWKS are registered with Lambda automatically at provisioning.
- **`lambda-pod-identity-webhook`** installed in the cluster (LKS ships it). The webhook watches for pods whose ServiceAccount is annotated `lambda.ai/identity-lrn` and mutates them to inject the projected token and identity env vars.
- **Workload identity enabled** for your Lambda account.
- A Lambda **admin API key** for the one-time operator setup below.

### 1. Create the identity and attach a trust (operator, out of band)

Using an admin key, create a service identity, add it to the workspace whose topology you will read, and assign it the `workload-identity` role (workspace-scoped `COMPUTE_INSTANCE_READ`). Note the returned **identity LRN** (`lrn:iam:identity:<id>`).

Then trust Topograph's ServiceAccount to assume that identity. LKS already registered the cluster's OIDC provider, but it does not trust any of your ServiceAccounts — that part is yours. Because there is no lookup-by-issuer API, re-posting the issuer and JWKS acts as an idempotent upsert that returns the provider LKS already registered:

```bash
# The cluster's issuer and public JWKS, read from the workload cluster.
ISS=$(kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)
kubectl get --raw /openid/v1/jwks > /tmp/jwks.json

# Idempotent upsert -> the existing provider_id for this issuer.
PID=$(curl -s -X POST -H "Authorization: Bearer $LAMBDA_ADMIN_TOKEN" \
  -H "Content-Type: application/json" "$URL/api/v1/oidc-providers" \
  -d "$(jq -n --arg iss "$ISS" --slurpfile jwks /tmp/jwks.json \
        '{issuer_url:$iss, jwks:$jwks[0]}')" | jq -r .data.provider_id)

# Add the trust. PATCH is additive, so other trusts are left alone.
curl -s -X PATCH -H "Authorization: Bearer $LAMBDA_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  "$URL/api/v1/identities/<identity-id>/oidc-trusts" \
  -d "$(jq -n --arg pid "$PID" \
        --arg sub "system:serviceaccount:<namespace>:<topograph-serviceaccount>" \
        '{trusts:[{provider_id:$pid, subject:$sub}]}')"
```

- `subject` is the projected token's `sub` claim, `system:serviceaccount:<namespace>:<serviceAccountName>`. Use `"*"` to trust any subject under the issuer (least restrictive).
- The provider upsert reports the `audience` it expects (`lambda-workload-identity`); the webhook stamps that same audience onto the projected token, so there is nothing to configure.

### 2. Configure Topograph

Annotate Topograph's ServiceAccount with the identity LRN, put `workspaceId` in the provider credentials, and set the API URL as a provider parameter — no Secret and no audience:

```yaml
provider:
  name: lambdai
  params:
    url: https://cloud.lambda.ai
  creds:
    workspaceId: "<WORKSPACE_ID>"

serviceAccount:
  annotations:
    lambda.ai/identity-lrn: "lrn:iam:identity:<id>"
```

`workspaceId` is an identifier, not a secret. It lives in `provider.creds`, where the Node Observer forwards it in topology requests, so no `config.credentialsSecret` is required. The webhook keys off the `lambda.ai/identity-lrn` annotation on the pod's ServiceAccount — there is no `workloadIdentity` parameter and no chart-managed volume. ServiceAccounts annotated before that key was renamed still work: the webhook reads the older `lambda.ai/role-lrn` when the current key is absent, so migrating is not urgent, but annotate new accounts with `lambda.ai/identity-lrn`.

### 3. Install with Helm (no credentials Secret)

```bash
helm install topograph oci://ghcr.io/dsx-ai-factory/topograph/topograph \
  --version <chart-version> -n topograph --create-namespace \
  -f values.k8s.lambdai-workload-identity-example.yaml
```

See [`values.k8s.lambdai-workload-identity-example.yaml`](../../charts/topograph/values.k8s.lambdai-workload-identity-example.yaml) for a complete example. Unlike the static-token flow, **no `config.credentialsSecret` is set**.

### How it works

Because the pod's ServiceAccount carries the `lambda.ai/identity-lrn` annotation, `lambda-pod-identity-webhook` mutates the pod to inject:

- a projected ServiceAccount token at `/var/run/secrets/lambda.ai/serviceaccount/token`, and
- the env vars `LAMBDA_IDENTITY_LRN` (the identity LRN) and `LAMBDA_WORKLOAD_IDENTITY_TOKEN_FILE` (the token path). The webhook also injects the deprecated `LAMBDA_ROLE_LRN` with the same value while operators migrate; Topograph reads it only when `LAMBDA_IDENTITY_LRN` is absent.

Topograph reads `LAMBDA_IDENTITY_LRN` to switch into workload-identity mode, reads the projected token from `LAMBDA_WORKLOAD_IDENTITY_TOKEN_FILE`, and exchanges it at `POST /api/v1/oidc/token` for a Lambda API key. The key is cached process-wide and refreshed shortly (≈5 minutes, jittered) before expiry so a fleet of pods does not refresh in lockstep. A transient exchange failure while the cached key is still valid is tolerated — Topograph keeps serving the current key until it actually expires. If the Lambda API rejects a cached key mid-life, Topograph mints a new one and retries the request once.

The pod identity is a fallback, not an override: a request (or `credentialsPath`) that supplies a `token` credential authenticates with **that** token even when the pod carries a workload identity, so a caller's credentials are never silently replaced by the pod's principal. Topograph logs which one it used. Supplying a `token` that is empty or whitespace is treated as a malformed credential and rejected with `400` — not as a request to fall back to the pod identity. To use workload identity, omit the `token` credential entirely.

### Caveats

- **Stable subject.** The trust pins the token's `sub` claim, `system:serviceaccount:<namespace>:<serviceAccountName>`. The chart's generated ServiceAccount name derives from the release name and can change; set a fixed `serviceAccount.name` and trust that exact subject (or use `"*"`) so the trust keeps matching.
- **JWKS rotation is handled by LKS.** LKS keeps the cluster's registered issuer and JWKS current, so key rotation does not require operator action.
- **Opaque failures.** The token-exchange endpoint returns an identical `401` for every failure (unknown issuer, untrusted subject, missing role). Topograph logs only the HTTP status and the identity LRN; check the pod logs for `workload-identity token exchange failed (status ...)` and verify the trust, subject, and role.
- **Injection that never happened.** A request that fails with `missing 'token' credential` in a deployment meant to use workload identity means the pod has no `LAMBDA_IDENTITY_LRN`, so the webhook did not mutate it. Mutating webhooks run only at pod creation, so annotating the ServiceAccount on a running release changes nothing until the pods restart; and with `serviceAccount.create=false` the chart skips the ServiceAccount template entirely, `serviceAccount.annotations` included, so the annotation has to be added to your existing account.

## Parameters

| Field | Required | Description |
|---|---|---|
| `url` | Yes | Base URL for the Lambda topology API, for example `https://cloud.example.com` |
| `trimTiers` | No | Number of highest topology tiers to trim from output. Defaults to `0` |

The region is **not** a parameter — it is taken from each entry in the request's `nodes` list and forwarded to the API as the `region` query parameter (the API requires it). The top-level Topograph `pageSize` setting controls the page size for paginated topology requests.

## Configuration

Example Topograph config for Slurm:

```yaml
http:
  port: 49021
  ssl: false

provider: lambdai
engine: slurm

requestAggregationDelay: 15s
pageSize: 200
credentialsPath: /etc/topograph/lambdai-credentials.yaml

providerParams:
  url: https://cloud.example.com

engineParams:
  plugin: topology/tree
  topologyConfigPath: /etc/slurm/topology.conf
```

Example request payload. The `nodes` list is required: each region maps provider instance IDs to the hostnames Topograph should emit.

```json
{
  "provider": {
    "name": "lambdai",
    "creds": {
      "workspaceId": "<WORKSPACE_ID>",
      "token": "<API_TOKEN>"
    },
    "params": {
      "url": "https://cloud.example.com"
    }
  },
  "engine": {
    "name": "slurm",
    "params": {
      "plugin": "topology/tree"
    }
  },
  "nodes": [
    {
      "region": "<REGION>",
      "instances": {
        "<INSTANCE_ID_1>": "node001",
        "<INSTANCE_ID_2>": "node002"
      }
    }
  ]
}
```

## How It Works

For each region in the request's `nodes` list, the provider pages through the topology endpoint:

```text
GET <url>/api/v1/topology/instance?workspace_id=<workspaceId>&region=<region>&page_size=<pageSize>
Authorization: Bearer <token>
```

The response is an envelope containing a `data` array and a pagination cursor:

```json
{
  "data": [
    { "id": "<instance-id>", "networkPath": [{ "id": "<switch>" }, { "id": "<switch>" }], "nvlink": null }
  ],
  "page_token": null
}
```

When `page_token` is non-null, the provider requests the next page with `&page_token=<token>` and repeats until it is null.

Each returned instance is translated as follows:

| API field | Topograph field |
|---|---|
| `id` | Instance ID (matched against the request's instance-to-hostname map) |
| `networkPath[0].id` | Leaf tier |
| `networkPath[1].id` | Spine tier |
| `networkPath[2].id` | Core tier |
| `nvlink.domain_id` + `nvlink.clique_id` | Accelerator / NVLink domain (`<domain_id>.<clique_id>`) |

`networkPath` is ordered from the leaf tier upward; paths shorter than three hops simply omit the higher tiers, and longer paths are logged and ignored.

NVLink domain data is best-effort. When the API returns `nvlink` for an instance, the provider derives its accelerator/NVLink domain, which enables `topology/block` output. When `nvlink` is null or absent, the provider emits the switch tree only. The exact shape of populated `nvlink` data may evolve; verify `topology/block` output once the API returns NVLink domains for your fleet.

## Kubernetes engine

With the `k8s` engine you do not pass `nodes` explicitly. Instead, the node-data-broker init container stamps each node with the two annotations the engine groups by, derived from fields the `lambda-cloud-controller` already sets on the Node object:

| Node field (set by `lambda-cloud-controller`) | Topograph annotation (set by node-data-broker) |
|---|---|
| `.spec.providerID` — `lambda://<instance-id>` | `topograph.run/instance` — `<instance-id>` (matches the API `id` 1:1) |
| `topology.kubernetes.io/region` label — e.g. `stg-sjc01-cl03` | `topograph.run/region` |

The Kubernetes engine then discovers nodes from these annotations, the provider queries the Lambda API once per region, and the engine writes `fabric.topograph.run/*` labels. The Node Observer re-triggers generation when nodes change.

Requirements:

- The `lambda-cloud-controller` must populate `.spec.providerID` and the `topology.kubernetes.io/region` label. The node-data-broker's init container errors and is retried by Kubernetes until both are present, so a node that is still initializing is simply labeled once its controller has finished.
- Keep the node-data-broker enabled (the chart default) — it is what translates the Node fields into the canonical annotations.
- **Node Observer trigger** — the Node Observer needs a watch selector or it crash-loops with `must specify nodeSelector and/or podSelector in trigger`. Set `nodeObserver.topograph.trigger.nodeSelector` (or `podSelector`) — e.g. `kubernetes.io/os: linux` to watch all nodes.
- **Tainted (GPU) nodes** — the node-data-broker is a DaemonSet and needs a matching toleration to run on tainted nodes (Lambda GPU instances carry `nvidia.com/gpu=true:NoSchedule`); without it those nodes are never annotated or labeled. `nodeDataBroker.tolerations[0].operator=Exists` lets it run on every node.
- **Image architecture** — the image must match the node architecture. Lambda GPU instances such as GH200 are `arm64`, so use a multi-arch or arm64 image.
- **Registry pull** — if the cluster cannot pull the image anonymously, create a pull secret and set the shared `imagePullSecrets` value.

Install with Helm (see the [Kubernetes quickstart](../get-started/quickstart-k8s.md) for the full flow):

```bash
# creds.yaml contains: workspaceId: <...>  /  token: <...>
kubectl create secret generic lambdai-creds \
  --from-file=credentials.yaml=creds.yaml -n topograph

helm install topograph oci://ghcr.io/dsx-ai-factory/topograph/topograph \
  --version <chart-version> -n topograph --create-namespace \
  --set provider.name=lambdai \
  --set provider.params.url=https://cloud.example.com \
  --set engine.name=k8s \
  --set config.credentialsSecret=lambdai-creds \
  --set "nodeObserver.topograph.trigger.nodeSelector.kubernetes\.io/os=linux" \
  --set "nodeDataBroker.tolerations[0].operator=Exists"

# After a few seconds, topology labels appear on nodes:
kubectl get nodes --show-labels | grep fabric.topograph.run
```

Only fabric `tier-N` labels appear until the API returns `nvlink` data (see the note above); the accelerator label follows once it does.

## Verifying the Output

First sanity-check the API directly:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "$URL/api/v1/topology/instance?workspace_id=$WORKSPACE_ID&region=$REGION" | jq .
```

Then trigger topology generation and read the result:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy for your Lambda instances. See the [Slurm engine documentation](../engines/slurm.md) for details.

## Simulation

A `lambdai-sim` provider variant is registered for testing without a live API. Instead of calling the topology API, it reads a YAML simulation model and serves it through the same translation path. Select it with `provider: lambdai-sim` and point it at a model file via the `modelFileName` parameter; see [Test Mode and Test Provider](./test.md) for the model-file format and simulation parameters.
