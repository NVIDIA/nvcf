{{/*
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
*/}}

{{/*
Name/label helpers for the in-house Cassandra chart resources
(StatefulSet, Services, hooks, etc). The fullname is fixed to "cassandra"
because initdb.sh and existing tooling reference the StatefulSet/pod
name "cassandra-0".
*/}}

{{- define "cassandra.name" -}}cassandra{{- end -}}

{{- define "cassandra.fullname" -}}cassandra{{- end -}}

{{- define "cassandra.labels" -}}
app.kubernetes.io/name: cassandra
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "cassandra.selectorLabels" -}}
app.kubernetes.io/name: cassandra
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Single namespace expression for every template in this chart (hooks,
Services, StatefulSet, ConfigMaps). Keeping one definition here prevents the
resources from landing in different namespaces when cassandra.namespace is
overridden.
*/}}

{{- define "cassandra.namespace" -}}
{{- .Values.cassandra.namespace | default .Release.Namespace -}}
{{- end -}}

{{/*
Comma-separated CASSANDRA_SEEDS value: the stable headless DNS names for the
first `.Values.cassandra.cluster.seed` StatefulSet ordinals. Static (not
runtime-discovered) so every pod's env is identical and seeds are reachable
before the rest of the cluster forms.
*/}}

{{- define "cassandra.seeds" -}}
{{- $svc := printf "%s-headless" (include "cassandra.fullname" .) -}}
{{- $ns := include "cassandra.namespace" . -}}
{{- $n := int .Values.cassandra.cluster.seed -}}
{{- $names := list -}}
{{- range $i := until $n -}}
{{- $names = append $names (printf "%s-%d.%s.%s.svc.cluster.local" (include "cassandra.fullname" $) $i $svc $ns) -}}
{{- end -}}
{{- /* extraSeeds bridges a second datacenter to an existing cluster: set it on
       the new datacenter to seed off a node in the existing datacenter. */ -}}
{{- range $s := .Values.cassandra.cluster.extraSeeds -}}
{{- $names = append $names $s -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}

{{/*
Return the proper Docker Image Registry Secret Names.
Supports both industry standard format and simplified format:
- Industry standard: [{name: "secret1"}, {name: "secret2"}]
- Simplified: ["secret1", "secret2"]
*/}}

{{- define "cassandra.imagePullSecrets" -}}
  {{- $pullSecrets := list }}

  {{- range .Values.cassandra.global.imagePullSecrets -}}
    {{- if kindIs "map" . -}}
      {{- $pullSecrets = append $pullSecrets .name -}}
    {{- else -}}
      {{- $pullSecrets = append $pullSecrets . -}}
    {{- end }}
  {{- end -}}

  {{- if (not (empty $pullSecrets)) -}}
imagePullSecrets:
    {{- range $pullSecrets | uniq }}
  - name: {{ . }}
    {{- end }}
  {{- end }}
{{- end -}}

{{/*
Resource request/limit presets by size tier.

The preset map and lookup below are derived from the Bitnami common Helm chart
template `common.resources.preset`:
https://github.com/bitnami/charts/tree/main/bitnami/common
Copyright Broadcom, Inc. Licensed under the Apache License, Version 2.0.

The limits are the requests increased by 50% (except ephemeral-storage and the
xlarge/2xlarge sizes). The stack passes the tier it wants via
cassandra.resourcesPreset (default xlarge = 3Gi request / 6Gi limit).
*/}}
{{- define "cassandra.resources.preset" -}}
{{- $presets := dict
  "nano" (dict "requests" (dict "cpu" "100m" "memory" "128Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "150m" "memory" "192Mi" "ephemeral-storage" "2Gi"))
  "micro" (dict "requests" (dict "cpu" "250m" "memory" "256Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "375m" "memory" "384Mi" "ephemeral-storage" "2Gi"))
  "small" (dict "requests" (dict "cpu" "500m" "memory" "512Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "750m" "memory" "768Mi" "ephemeral-storage" "2Gi"))
  "medium" (dict "requests" (dict "cpu" "500m" "memory" "1024Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "750m" "memory" "1536Mi" "ephemeral-storage" "2Gi"))
  "large" (dict "requests" (dict "cpu" "1.0" "memory" "2048Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "1.5" "memory" "3072Mi" "ephemeral-storage" "2Gi"))
  "xlarge" (dict "requests" (dict "cpu" "1.0" "memory" "3072Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "3.0" "memory" "6144Mi" "ephemeral-storage" "2Gi"))
  "2xlarge" (dict "requests" (dict "cpu" "1.0" "memory" "3072Mi" "ephemeral-storage" "50Mi") "limits" (dict "cpu" "6.0" "memory" "12288Mi" "ephemeral-storage" "2Gi"))
 -}}
{{- if hasKey $presets .type -}}
{{- index $presets .type | toYaml -}}
{{- else -}}
{{- printf "ERROR: cassandra.resourcesPreset '%s' invalid. Allowed values are %s" .type (join "," (keys $presets)) | fail -}}
{{- end -}}
{{- end -}}

{{/*
Effective resources for the cassandra container: an explicit cassandra.resources
map wins; otherwise a non-"none" cassandra.resourcesPreset is expanded; otherwise
empty. An explicit resources map wins over a preset so the stack can keep passing resourcesPreset.
*/}}
{{- define "cassandra.resources" -}}
{{- if .Values.cassandra.resources -}}
{{- toYaml .Values.cassandra.resources -}}
{{- else if and .Values.cassandra.resourcesPreset (ne .Values.cassandra.resourcesPreset "none") -}}
{{- include "cassandra.resources.preset" (dict "type" .Values.cassandra.resourcesPreset) -}}
{{- end -}}
{{- end -}}

{{/*
Effective MAX_HEAP_SIZE. An explicit cassandra.jvm.maxHeapSize wins. Otherwise,
when a resourcesPreset is set, derive ~50% of that preset's memory limit so the
official image (which auto-sizes heap from HOST memory, ignoring the pod limit)
does not oversubscribe and OOM. Empty means let the image auto-size (single-node
or dev only). Returns an empty string when nothing applies.
*/}}
{{- define "cassandra.maxHeapSize" -}}
{{- if .Values.cassandra.jvm.maxHeapSize -}}
{{- .Values.cassandra.jvm.maxHeapSize -}}
{{- else if and .Values.cassandra.resourcesPreset (ne .Values.cassandra.resourcesPreset "none") -}}
{{- $heap := dict "nano" "96M" "micro" "192M" "small" "384M" "medium" "768M" "large" "1536M" "xlarge" "3072M" "2xlarge" "6144M" -}}
{{- index $heap .Values.cassandra.resourcesPreset -}}
{{- end -}}
{{- end -}}
