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

{{- define "nv_api_keys.name" -}}
{{- default .Chart.Name .Values.apikeys.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}

{{- define "nv_api_keys.fullname" -}}
{{- if .Values.apikeys.fullnameOverride }}
{{- .Values.apikeys.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.apikeys.nameOverride }}
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

{{- define "nv_api_keys.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}

{{- define "nv_api_keys.namespace" -}}
{{- default .Release.Namespace .Values.apikeys.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}

{{- define "nv_api_keys.image" -}}
{{- $registry := required "A valid image registry (.Values.apikeys.image.registry) is required!" .Values.apikeys.image.registry -}}
{{- $repository := required "A valid image repository (.Values.apikeys.image.repository) is required!" .Values.apikeys.image.repository -}}
{{- $tag := .Values.apikeys.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}

{{- define "nv_api_keys.labels" -}}
helm.sh/chart: {{ include "nv_api_keys.chart" . }}
{{ include "nv_api_keys.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}

{{- define "nv_api_keys.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nv_api_keys.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}

{{- define "nv_api_keys.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "api-keys-api"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl" 
vault.hashicorp.com/secret-volume-path: "/home/app/vault"
{{- end }}
