{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/ -}}
{{/*
Create a component fullname from the root chart fullname. Truncate the root
portion first so the component suffix remains intact within the 63-character
Kubernetes DNS-label limit.
*/}}
{{- define "nodeDataBroker.fullname" -}}
{{- $base := include "topograph.fullname" . | trunc 46 | trimSuffix "-" -}}
{{- printf "%s-node-data-broker" $base -}}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nodeDataBroker.chart" -}}
{{- printf "node-data-broker-%s" .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nodeDataBroker.labels" -}}
helm.sh/chart: {{ include "nodeDataBroker.chart" . }}
{{ include "nodeDataBroker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nodeDataBroker.selectorLabels" -}}
app.kubernetes.io/name: node-data-broker
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "nodeDataBroker.serviceAccountName" -}}
{{- if .Values.nodeDataBroker.serviceAccount.create }}
{{- default (include "nodeDataBroker.fullname" .) .Values.nodeDataBroker.serviceAccount.name }}
{{- else }}
{{- required "nodeDataBroker.serviceAccount.name must be set when nodeDataBroker.serviceAccount.create=false" .Values.nodeDataBroker.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the RBAC resources.
*/}}
{{- define "nodeDataBroker.rbacName" -}}
{{- include "nodeDataBroker.fullname" . }}
{{- end }}

{{/* Resolve the configured accelerator source. */}}
{{- define "nodeDataBroker.acceleratorSource" -}}
{{- $providerParams := default dict .Values.provider.params -}}
{{- $acceleratorValue := get $providerParams "accelerator" -}}
{{- $accelerator := default dict $acceleratorValue -}}
{{- $source := "none" -}}
{{- if hasKey $providerParams "accelerator" -}}
{{- if and (kindIs "map" $acceleratorValue) (eq (len $acceleratorValue) 0) -}}
{{- $source = "none" -}}
{{- else -}}
{{- $source = get $accelerator "source" -}}
{{- if empty $source -}}
{{- fail "provider.params.accelerator.source must be set when provider.params.accelerator is present" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- lower (toString $source) -}}
{{- end }}

{{/*
Render the provider configuration used by node-data-broker. The broker needs
the GPU Operator workload location when nvidia-smi discovery is enabled, so
materialize its defaults in the generated configuration while preserving
explicit overrides.
*/}}
{{- define "nodeDataBroker.providerConfig" -}}
{{- $provider := deepCopy .Values.provider -}}
{{- if eq (include "nodeDataBroker.acceleratorSource" .) "nvidia-smi" -}}
{{- $params := default dict (get $provider "params") -}}
{{- $accelerator := default dict (get $params "accelerator") -}}
{{- $nvidiaSmi := default dict (get $accelerator "nvidiaSmi") -}}
{{- if empty (trim (toString (get $nvidiaSmi "gpuOperatorNamespace"))) -}}
{{- $_ := set $nvidiaSmi "gpuOperatorNamespace" "gpu-operator" -}}
{{- end -}}
{{- if empty (trim (toString (get $nvidiaSmi "devicePluginDaemonSet"))) -}}
{{- $_ := set $nvidiaSmi "devicePluginDaemonSet" "nvidia-device-plugin-daemonset" -}}
{{- end -}}
{{- $_ := set $accelerator "nvidiaSmi" $nvidiaSmi -}}
{{- $_ := set $params "accelerator" $accelerator -}}
{{- $_ := set $provider "params" $params -}}
{{- end -}}
{{- toYaml $provider -}}
{{- end }}

{{/*
Create the name of a generated ConfigMap mount.
*/}}
{{- define "nodeDataBroker.configMapMountName" -}}
{{- $root := .root -}}
{{- $name := required "nodeDataBroker.configMapMounts[].name is required" .name | lower | replace "_" "-" -}}
{{- printf "%s-%s" (include "nodeDataBroker.fullname" $root) $name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create the volume name for a generated ConfigMap mount.
*/}}
{{- define "nodeDataBroker.configMapMountVolumeName" -}}
{{- $name := required "nodeDataBroker.configMapMounts[].name is required" .name | lower | replace "_" "-" -}}
{{- printf "config-map-%s" $name | trunc 63 | trimSuffix "-" }}
{{- end }}
