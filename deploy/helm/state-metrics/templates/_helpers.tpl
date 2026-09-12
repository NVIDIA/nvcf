{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{- define "nvcf-state-metrics.name" -}}
{{- default .Chart.Name .Values.stateMetrics.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "nvcf-state-metrics.fullname" -}}
{{- if .Values.stateMetrics.fullnameOverride }}
{{- .Values.stateMetrics.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.stateMetrics.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "nvcf-state-metrics.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "nvcf-state-metrics.namespace" -}}
{{- default .Release.Namespace .Values.stateMetrics.namespace -}}
{{- end }}

{{- define "nvcf-state-metrics.image" -}}
{{- $registry := required "A valid image registry (.Values.stateMetrics.image.registry) is required!" .Values.stateMetrics.image.registry -}}
{{- $repository := required "A valid image repository (.Values.stateMetrics.image.repository) is required!" .Values.stateMetrics.image.repository -}}
{{- $tag := .Values.stateMetrics.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end }}

{{- define "nvcf-state-metrics.labels" -}}
helm.sh/chart: {{ include "nvcf-state-metrics.chart" . }}
{{ include "nvcf-state-metrics.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "nvcf-state-metrics.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-state-metrics.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "nvcf-state-metrics.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "nvcf-state-metrics"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-nvcf_jwt_token.txt: "/vault/config/templates/nvcf_jwt_token.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}
