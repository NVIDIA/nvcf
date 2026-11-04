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
{{- define "nvstor-server.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "nvstor-server.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nvstor-server.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvstor-server.labels" -}}
helm.sh/chart: {{ include "nvstor-server.chart" . }}
{{ include "nvstor-server.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvstor-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvstor-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Headless service name
*/}}
{{- define "nvstor-server.headlessServiceName" -}}
{{- include "nvstor-server.fullname" . }}-headless
{{- end }}

{{/*
Generate cluster seeds based on replica count
*/}}
{{- define "nvstor-server.clusterSeeds" -}}
{{- $fullname := include "nvstor-server.fullname" . -}}
{{- $headless := include "nvstor-server.headlessServiceName" . -}}
{{- $namespace := .Release.Namespace -}}
{{- $raftPort := .Values.service.raft.port -}}
{{- range $i := until (int .Values.replicaCount) }}
"{{ $fullname }}-{{ $i }}.{{ $headless }}.{{ $namespace }}.svc.cluster.local:{{ $raftPort }}",
{{- end }}
{{- end }}
