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

{{- define "setReplicas" -}}
{{- $containers := index . "containers" -}}
{{- $wlSpecs := index . "wlSpecs" -}}
{{- range $containerName, $containerData := $containers }}
    {{- if and ($wlSpecs) ($containerData.workload) }}
    {{- $replicaCount := index $wlSpecs $containerData.workload  "wl_units"  }}
replicas: {{ $replicaCount }}
    {{- end }}
{{- end }}
{{- end }}

{{/*
Expand the name of the chart.
*/}}
{{- define "std-helm.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "std-helm.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "std-helm.labels" -}}
helm.sh/chart: {{ include "std-helm.chart" . }}
{{ include "std-helm.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "std-helm.selectorLabels" -}}
app.kubernetes.io/name: {{ include "std-helm.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}