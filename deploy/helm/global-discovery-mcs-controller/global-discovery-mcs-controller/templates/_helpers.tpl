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
{{- define "gds-mcs-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name, truncated to the 63 character DNS
label limit. A release name that contains the chart name is used as is.
*/}}
{{- define "gds-mcs-controller.fullname" -}}
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
Name of an object derived from the full name with a suffix, within the DNS
label limit. Usage: include "gds-mcs-controller.suffixed" (list . "metrics")
*/}}
{{- define "gds-mcs-controller.suffixed" -}}
{{- $root := index . 0 -}}
{{- $suffix := index . 1 -}}
{{- $max := sub 62 (len $suffix) | int -}}
{{- printf "%s-%s" (include "gds-mcs-controller.fullname" $root | trunc $max | trimSuffix "-") $suffix -}}
{{- end -}}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "gds-mcs-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "gds-mcs-controller.labels" -}}
helm.sh/chart: {{ include "gds-mcs-controller.chart" . }}
{{ include "gds-mcs-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/component: mcs-controller
app.kubernetes.io/part-of: global-discovery
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "gds-mcs-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gds-mcs-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the controller ServiceAccount.
*/}}
{{- define "gds-mcs-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "gds-mcs-controller.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Controller image. The repository is required, the tag defaults to the chart
appVersion, and a sha256: tag is used as a digest.
*/}}
{{- define "gds-mcs-controller.image" -}}
{{- $repository := required "image.repository is required" .Values.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag | toString -}}
{{- if hasPrefix "sha256:" $tag -}}
{{- printf "%s@%s" $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
The identity source, flags unless identity.source is set.
*/}}
{{- define "gds-mcs-controller.identitySource" -}}
{{- .Values.identity.source | default "flags" -}}
{{- end -}}

{{/*
Mount path of the GDS CA ConfigMap.
*/}}
{{- define "gds-mcs-controller.gdsCaDir" -}}
/etc/gds/ca
{{- end -}}

{{/*
OTEL_RESOURCE_ATTRIBUTES: the pod and namespace, then the extra attributes in
key order.
*/}}
{{- define "gds-mcs-controller.otelResourceAttributes" -}}
{{- $attrs := list "k8s.pod.name=$(POD_NAME)" "k8s.namespace.name=$(POD_NAMESPACE)" -}}
{{- range $key := keys .Values.otel.resourceAttributes | sortAlpha -}}
{{- $attrs = append $attrs (printf "%s=%v" $key (index $.Values.otel.resourceAttributes $key)) -}}
{{- end -}}
{{- join "," $attrs -}}
{{- end -}}

{{/*
gds-mcs-controller arguments, one YAML list item per flag. Boolean flags are
always rendered with their value, so a chart value never depends on a
controller default. The leader election namespace comes from POD_NAMESPACE.
*/}}
{{- define "gds-mcs-controller.args" -}}
{{- range .Values.gds.urls }}
- {{ printf "--gds-url=%s" . | quote }}
{{- end }}
{{- if .Values.gds.tlsCaConfigMap }}
- {{ printf "--gds-tls-ca-file=%s/ca.crt" (include "gds-mcs-controller.gdsCaDir" .) | quote }}
{{- end }}
- {{ printf "--gds-allow-insecure-http=%t" (.Values.gds.allowInsecureHttp | default false) | quote }}
- {{ printf "--cluster-identity-source=%s" (include "gds-mcs-controller.identitySource" .) | quote }}
{{- with .Values.clusterset.id }}
- {{ printf "--clusterset-id=%s" . | quote }}
{{- end }}
{{- with .Values.cluster.id }}
- {{ printf "--cluster-id=%s" . | quote }}
{{- end }}
{{- with .Values.cluster.region }}
- {{ printf "--region=%s" . | quote }}
{{- end }}
{{- with .Values.cluster.labels }}
{{- $pairs := list }}
{{- range $key := keys . | sortAlpha }}
{{- $pairs = append $pairs (printf "%s=%v" $key (index $.Values.cluster.labels $key)) }}
{{- end }}
- {{ printf "--cluster-labels=%s" (join "," $pairs) | quote }}
{{- end }}
- {{ printf "--write-cluster-properties=%t" (.Values.identity.writeClusterProperties | default false) | quote }}
- {{ printf "--lease-ttl=%s" .Values.lease.ttl | quote }}
- {{ printf "--deregister-on-shutdown=%t" (.Values.deregisterOnShutdown | default false) | quote }}
- {{ printf "--create-namespaces=%t" (.Values.imports.createNamespaces | default false) | quote }}
- {{ printf "--adopt-unmanaged-imports=%t" (.Values.imports.adoptUnmanaged | default false) | quote }}
- {{ printf "--auto-export-pools=%t" (.Values.pools.autoExport | default false) | quote }}
{{- with .Values.watchNamespaces }}
- {{ printf "--watch-namespaces=%s" (join "," .) | quote }}
{{- end }}
- {{ printf "--endpoints-per-slice=%d" (int .Values.endpointsPerSlice) | quote }}
- {{ printf "--max-concurrent-reconciles=%d" (int .Values.maxConcurrentReconciles) | quote }}
- {{ printf "--metrics-bind-address=:%d" (int .Values.metrics.port) | quote }}
- {{ printf "--health-probe-bind-address=:%d" (int .Values.healthProbe.port) | quote }}
- {{ printf "--leader-elect=%t" (.Values.leaderElection.enabled | default false) | quote }}
- {{ printf "--otel-enabled=%t" (.Values.otel.enabled | default false) | quote }}
- {{ printf "--zap-log-level=%s" .Values.logLevel | quote }}
{{- range .Values.extraArgs }}
- {{ . | quote }}
{{- end }}
{{- end -}}

{{/*
Validate the values the controller needs to start. Every missing required
value is reported at once. Renders nothing.
*/}}
{{- define "gds-mcs-controller.validate" -}}
{{- $missing := list -}}
{{- if not .Values.image.repository -}}
{{- $missing = append $missing "image.repository" -}}
{{- end -}}
{{- if not .Values.gds.urls -}}
{{- $missing = append $missing "gds.urls" -}}
{{- end -}}
{{- if eq (include "gds-mcs-controller.identitySource" .) "flags" -}}
{{- if not .Values.clusterset.id -}}
{{- $missing = append $missing "clusterset.id" -}}
{{- end -}}
{{- if not .Values.cluster.id -}}
{{- $missing = append $missing "cluster.id" -}}
{{- end -}}
{{- end -}}
{{- if $missing -}}
{{- fail (printf "global-discovery-mcs-controller: set the required values: %s" (join ", " $missing)) -}}
{{- end -}}
{{- $schemes := dict -}}
{{- range .Values.gds.urls -}}
{{- $_ := set $schemes (regexFind "^https?" .) true -}}
{{- end -}}
{{- if gt (len $schemes) 1 -}}
{{- fail "global-discovery-mcs-controller: gds.urls must all use one scheme, http:// or https://" -}}
{{- end -}}
{{- if and (hasKey $schemes "http") (not .Values.gds.allowInsecureHttp) -}}
{{- fail "global-discovery-mcs-controller: gds.urls uses http://; set gds.allowInsecureHttp=true (development only) or use https://" -}}
{{- end -}}
{{- if and (not .Values.leaderElection.enabled) (gt (int .Values.replicaCount) 1) -}}
{{- fail "global-discovery-mcs-controller: replicaCount above 1 needs leaderElection.enabled, or two controllers would write to GDS at once" -}}
{{- end -}}
{{- if eq (int .Values.metrics.port) (int .Values.healthProbe.port) -}}
{{- fail "global-discovery-mcs-controller: metrics.port and healthProbe.port must differ" -}}
{{- end -}}
{{- if .Values.podDisruptionBudget.enabled -}}
{{- $min := toString .Values.podDisruptionBudget.minAvailable -}}
{{- $max := toString .Values.podDisruptionBudget.maxUnavailable -}}
{{- if or (and (ne $min "") (ne $max "")) (and (eq $min "") (eq $max "")) -}}
{{- fail "global-discovery-mcs-controller: podDisruptionBudget: set exactly one of minAvailable or maxUnavailable" -}}
{{- end -}}
{{- end -}}
{{- end -}}
