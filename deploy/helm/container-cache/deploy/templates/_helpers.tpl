{{/*
SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
{{- define "nvcf-container-cache.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-container-cache.fullname" -}}
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
{{- define "nvcf-container-cache.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvcf-container-cache.labels" -}}
helm.sh/chart: {{ include "nvcf-container-cache.chart" . }}
{{ include "nvcf-container-cache.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-container-cache.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-container-cache.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "nvcf-container-cache.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "nvcf-container-cache.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Compute CRI-O registry port mappings.
If .Values.crio.registryPorts is set, use it.
Otherwise, derive ports from targetHost list starting at basePort.
*/}}
{{- define "nvcf-container-cache.crioRegistryPorts" -}}
{{- if and .Values.crio.registryPorts (gt (len .Values.crio.registryPorts) 0) -}}
{{- range .Values.crio.registryPorts }}
{{ .name }}:
  registry: {{ .registry }}
  port: {{ .port }}
{{- end }}
{{- else -}}
{{- $base := (.Values.crio.basePort | default (add (.Values.service.port | default 30345) 1)) -}}
{{- $hosts := splitList "," (.Values.targetHost | default "nvcr.io") -}}
{{- range $i, $host := $hosts }}
{{ $host | lower | replace "." "-" | replace "/" "-" | trunc 10 | trimSuffix "-" }}:
  registry: {{ $host }}
  port: {{ add $base $i }}
{{- end }}
{{- end }}
{{- end }}
