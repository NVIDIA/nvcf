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
{{- define "reval.name" -}}
{{- default .Chart.Name .Values.reval.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "reval.fullname" -}}
{{- if .Values.reval.fullnameOverride }}
{{- .Values.reval.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.reval.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "reval.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "reval.labels" -}}
helm.sh/chart: {{ include "reval.chart" . }}
{{ include "reval.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "reval.selectorLabels" -}}
app.kubernetes.io/name: {{ include "reval.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "reval.serviceAccountName" -}}
{{- if .Values.reval.serviceAccount.create }}
{{- default (include "reval.fullname" .) .Values.reval.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.reval.serviceAccount.name }}
{{- end }}
{{- end }}
