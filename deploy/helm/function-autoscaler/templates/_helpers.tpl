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
{{- define "function-autoscaler.name" -}}
{{- default .Chart.Name .Values.functionautoscaler.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "function-autoscaler.fullname" -}}
{{- if .Values.functionautoscaler.fullnameOverride }}
{{- .Values.functionautoscaler.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.functionautoscaler.nameOverride }}
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
{{- define "function-autoscaler.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Resolve the deployment namespace. Default to nvcf because the OpenBao JWT auth
role is bound to the nvcf-autoscaler-service service account in that namespace.
*/}}
{{- define "function-autoscaler.namespace" -}}
{{- default "nvcf" .Values.functionautoscaler.namespace -}}
{{- end -}}

{{/*
Derive the full image value. Registry may be empty for local images (e.g. rs-autoscaler:local).
*/}}
{{- define "function-autoscaler.image" -}}
{{- $registry := .Values.functionautoscaler.image.registry | default "" -}}
{{- $repository := required "A valid image repository (.Values.functionautoscaler.image.repository) is required!" .Values.functionautoscaler.image.repository -}}
{{- $tag := .Values.functionautoscaler.image.tag | default .Chart.AppVersion -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Validate the service account used by the deployment and OpenBao JWT auth.
*/}}
{{- define "function-autoscaler.serviceAccountName" -}}
{{- required "A valid service account name (.Values.functionautoscaler.serviceAccountName) is required!" .Values.functionautoscaler.serviceAccountName -}}
{{- end -}}

{{/*
Resolve the Service name. The default is stable for existing DNS, but callers
can override it when running multiple releases in one namespace.
*/}}
{{- define "function-autoscaler.serviceName" -}}
{{- default "function-autoscaler" .Values.functionautoscaler.service.name -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "function-autoscaler.labels" -}}
helm.sh/chart: {{ include "function-autoscaler.chart" . }}
{{ include "function-autoscaler.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "function-autoscaler.selectorLabels" -}}
app.kubernetes.io/name: {{ include "function-autoscaler.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Vault Agent Injector annotations for JWT auth (self-hosted OpenBao).
The rendered secrets.json is written to /vault/secrets and read via
SECRETS_PATH. agent-copy-volume-mounts copies the app container's
volume mounts (including the projected service account token) onto the agent.
*/}}
{{- define "function-autoscaler.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ .Values.functionautoscaler.vault.role | quote }}
vault.hashicorp.com/auth-path: {{ .Values.functionautoscaler.vault.authPath | quote }}
vault.hashicorp.com/agent-copy-volume-mounts: "function-autoscaler"
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}
