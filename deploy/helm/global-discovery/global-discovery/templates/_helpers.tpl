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
Expand the name of the chart.
*/}}
{{- define "global-discovery.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name, truncated to the 63 character DNS
label limit. A release name that contains the chart name is used as is.
*/}}
{{- define "global-discovery.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Name of the optional LoadBalancer Service. The suffix stays within the DNS
label limit.
*/}}
{{- define "global-discovery.externalServiceName" -}}
{{- printf "%s-external" (include "global-discovery.fullname" . | trunc 54 | trimSuffix "-") -}}
{{- end -}}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "global-discovery.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "global-discovery.labels" -}}
helm.sh/chart: {{ include "global-discovery.chart" . }}
{{ include "global-discovery.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/component: server
app.kubernetes.io/part-of: global-discovery
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "global-discovery.selectorLabels" -}}
app.kubernetes.io/name: {{ include "global-discovery.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the server ServiceAccount.
*/}}
{{- define "global-discovery.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "global-discovery.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Server image. The repository is required, the tag defaults to the chart
appVersion, and a sha256: tag is used as a digest.
*/}}
{{- define "global-discovery.image" -}}
{{- $repository := required "image.repository is required" .Values.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag | toString -}}
{{- if hasPrefix "sha256:" $tag -}}
{{- printf "%s@%s" $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Replicas the release runs at rest: autoscaling.minReplicas with the HPA,
replicaCount otherwise.
*/}}
{{- define "global-discovery.effectiveReplicas" -}}
{{- if .Values.autoscaling.enabled -}}
{{- int .Values.autoscaling.minReplicas -}}
{{- else -}}
{{- int .Values.replicaCount -}}
{{- end -}}
{{- end -}}

{{/*
OTEL_RESOURCE_ATTRIBUTES: the pod, namespace and region, then the extra
attributes in key order.
*/}}
{{- define "global-discovery.otelResourceAttributes" -}}
{{- $attrs := list "k8s.pod.name=$(POD_NAME)" "k8s.namespace.name=$(POD_NAMESPACE)" (printf "cloud.region=%s" .Values.region) -}}
{{- range $key := keys .Values.otel.resourceAttributes | sortAlpha -}}
{{- $attrs = append $attrs (printf "%s=%v" $key (index $.Values.otel.resourceAttributes $key)) -}}
{{- end -}}
{{- join "," $attrs -}}
{{- end -}}

{{/*
gds-server arguments, one YAML list item per flag. The region reaches the
server through GDS_REGION.
*/}}
{{- define "global-discovery.args" -}}
- {{ printf "--grpc-listen-address=:%d" (int .Values.ports.grpc) | quote }}
- {{ printf "--http-listen-address=:%d" (int .Values.ports.http) | quote }}
- {{ printf "--admin-listen-address=:%d" (int .Values.ports.admin) | quote }}
- {{ printf "--store=%s" .Values.store.kind | quote }}
{{- if eq .Values.store.kind "dynamodb" }}
- {{ printf "--dynamodb-table=%s" .Values.store.dynamodb.table | quote }}
{{- with .Values.store.dynamodb.endpoint }}
- {{ printf "--dynamodb-endpoint=%s" . | quote }}
{{- end }}
{{- end }}
- {{ printf "--refresh-interval=%s" .Values.refreshInterval | quote }}
- {{ printf "--clusterset-discovery-interval=%s" .Values.clustersetDiscoveryInterval | quote }}
- {{ printf "--lease-skew=%s" .Values.lease.skew | quote }}
- {{ printf "--lease-eval-interval=%s" .Values.lease.evalInterval | quote }}
- {{ printf "--retention=%s" .Values.retention | quote }}
- {{ printf "--compaction-interval=%s" .Values.compactionInterval | quote }}
- {{ printf "--bookmark-interval=%s" .Values.watch.bookmarkInterval | quote }}
- {{ printf "--watch-history=%d" (int .Values.watch.history) | quote }}
- {{ printf "--watch-queue-size=%d" (int .Values.watch.queueSize) | quote }}
- {{ printf "--max-watchers=%d" (int .Values.watch.maxWatchers) | quote }}
{{- with .Values.advertiseEndpoints }}
- {{ printf "--advertise-endpoints=%s" (toJson .) | quote }}
{{- end }}
{{- if .Values.tls.existingSecret }}
- "--tls-cert-file=/etc/gds/tls/tls.crt"
- "--tls-key-file=/etc/gds/tls/tls.key"
{{- end }}
- {{ printf "--shutdown-drain=%s" .Values.shutdown.drain | quote }}
- {{ printf "--shutdown-timeout=%s" .Values.shutdown.timeout | quote }}
{{- if .Values.otel.enabled }}
- "--otel-enabled=true"
{{- end }}
- {{ printf "--log-level=%s" .Values.logLevel | quote }}
{{- range .Values.extraArgs }}
- {{ . | quote }}
{{- end }}
{{- end -}}

{{/*
Validate the values the server needs to start. Every missing required value
is reported at once. Renders nothing.
*/}}
{{- define "global-discovery.validate" -}}
{{- $missing := list -}}
{{- if not .Values.image.repository -}}
{{- $missing = append $missing "image.repository" -}}
{{- end -}}
{{- if not .Values.region -}}
{{- $missing = append $missing "region" -}}
{{- end -}}
{{- if and (eq .Values.store.kind "dynamodb") (not .Values.store.dynamodb.table) -}}
{{- $missing = append $missing "store.dynamodb.table" -}}
{{- end -}}
{{- if $missing -}}
{{- fail (printf "global-discovery: set the required values: %s" (join ", " $missing)) -}}
{{- end -}}
{{- if and (eq .Values.store.kind "memory") (or .Values.autoscaling.enabled (gt (int .Values.replicaCount) 1)) -}}
{{- fail "global-discovery: store.kind=memory keeps the registry in each pod, so it needs replicaCount 1 and autoscaling.enabled=false; use store.kind=dynamodb for more replicas" -}}
{{- end -}}
{{- if and .Values.autoscaling.enabled (gt (int .Values.autoscaling.minReplicas) (int .Values.autoscaling.maxReplicas)) -}}
{{- fail "global-discovery: autoscaling.minReplicas must not exceed autoscaling.maxReplicas" -}}
{{- end -}}
{{- $ports := list (int .Values.ports.grpc) (int .Values.ports.http) (int .Values.ports.admin) -}}
{{- if ne (len (uniq $ports)) 3 -}}
{{- fail "global-discovery: ports.grpc, ports.http and ports.admin must differ" -}}
{{- end -}}
{{- if .Values.podDisruptionBudget.enabled -}}
{{- $min := toString .Values.podDisruptionBudget.minAvailable -}}
{{- $max := toString .Values.podDisruptionBudget.maxUnavailable -}}
{{- if or (and (ne $min "") (ne $max "")) (and (eq $min "") (eq $max "")) -}}
{{- fail "global-discovery: podDisruptionBudget: set exactly one of minAvailable or maxUnavailable" -}}
{{- end -}}
{{- end -}}
{{- $primaries := 0 -}}
{{- range $i, $ep := .Values.advertiseEndpoints -}}
{{- if not $ep.region -}}
{{- fail (printf "global-discovery: advertiseEndpoints[%d].region is required" $i) -}}
{{- end -}}
{{- if and (not $ep.grpcUrl) (not $ep.httpUrl) -}}
{{- fail (printf "global-discovery: advertiseEndpoints[%d] needs grpcUrl or httpUrl" $i) -}}
{{- end -}}
{{- if $ep.primary -}}
{{- $primaries = add1 $primaries -}}
{{- end -}}
{{- end -}}
{{- if gt $primaries 1 -}}
{{- fail "global-discovery: at most one advertiseEndpoints entry may be primary" -}}
{{- end -}}
{{- end -}}
