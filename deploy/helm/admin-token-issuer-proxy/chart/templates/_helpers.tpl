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

{{- define "admin-issuer-proxy.name" -}}
{{- default .Chart.Name .Values.adminIssuerProxy.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}

{{- define "admin-issuer-proxy.fullname" -}}
{{- if .Values.adminIssuerProxy.fullnameOverride }}
{{- .Values.adminIssuerProxy.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.adminIssuerProxy.nameOverride }}
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

{{- define "admin-issuer-proxy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Derive the full image value
*/}}

{{- define "admin-issuer-proxy.image" -}}
{{- $registry := required "A valid image registry (.Values.adminIssuerProxy.image.registry) is required!" .Values.adminIssuerProxy.image.registry -}}
{{- $repository := required "A valid image repository (.Values.adminIssuerProxy.image.repository) is required!" .Values.adminIssuerProxy.image.repository -}}
{{- $tag := .Values.adminIssuerProxy.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}

{{- define "admin-issuer-proxy.labels" -}}
helm.sh/chart: {{ include "admin-issuer-proxy.chart" . }}
{{ include "admin-issuer-proxy.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}

{{- define "admin-issuer-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "admin-issuer-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}

{{- define "admin-issuer-proxy.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "admin-issuer-proxy"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-token: "true"
{{- end }}
