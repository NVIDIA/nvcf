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
{{- define "llm-api-gateway.name" -}}
{{- default .Chart.Name .Values.llmApiGateway.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "llm-api-gateway.fullname" -}}
{{- if .Values.llmApiGateway.fullnameOverride }}
{{- .Values.llmApiGateway.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- include "llm-api-gateway.name" . }}
{{- end }}
{{- end }}

{{/*
Chart label.
*/}}
{{- define "llm-api-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "llm-api-gateway.labels" -}}
helm.sh/chart: {{ include "llm-api-gateway.chart" . }}
{{ include "llm-api-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "llm-api-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "llm-api-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Allow the release namespace to be overridden.
*/}}
{{- define "llm-api-gateway.namespace" -}}
{{- default .Release.Namespace .Values.llmApiGateway.namespace -}}
{{- end -}}

{{/*
Service account name
*/}}
{{- define "llm-api-gateway.serviceAccountName" -}}
{{- if .Values.llmApiGateway.serviceAccount.create }}
{{- default (include "llm-api-gateway.fullname" .) .Values.llmApiGateway.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.llmApiGateway.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference
*/}}
{{- define "llm-api-gateway.image" -}}
{{- $registry := .Values.llmApiGateway.image.registry -}}
{{- $repository := required "llmApiGateway.image.repository is required" .Values.llmApiGateway.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.llmApiGateway.image.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{/*
Olric Kubernetes label selector
*/}}
{{- define "llm-api-gateway.olric.labelSelector" -}}
{{- default (printf "app.kubernetes.io/name=%s" (include "llm-api-gateway.name" .)) .Values.llmApiGateway.olric.k8sLabelSelector -}}
{{- end }}

{{/*
Config checksum
*/}}
{{- define "llm-api-gateway.configChecksum" -}}
{{- $tracked := dict "config" .Values.llmApiGateway.config "observability" .Values.llmApiGateway.observability "olric" .Values.llmApiGateway.olric -}}
{{- toYaml $tracked | sha256sum -}}
{{- end }}

{{/*
Vault audience
*/}}
{{- define "llm-api-gateway.vaultAudience" -}}
{{- .Values.llmApiGateway.vault.audience | default "http://openbao-server.vault-system.svc.cluster.local:8200" -}}
{{- end }}

{{/*
Vault Annotations
*/}}
{{- define "llm-api-gateway.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ if .Values.llmApiGateway.vault }}{{ .Values.llmApiGateway.vault.vaultRole | default "llm-api-gateway" }}{{ else }}"llm-api-gateway"{{ end }}
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
{{- if .Values.llmApiGateway.vault }}
{{- if .Values.llmApiGateway.vault.jwtAuthPath }}
vault.hashicorp.com/jwt-auth-path: {{ .Values.llmApiGateway.vault.jwtAuthPath }}
{{- end }}
{{- if .Values.llmApiGateway.vault.vaultAddress }}
vault.hashicorp.com/service: {{ .Values.llmApiGateway.vault.vaultAddress }}
{{- end }}
{{- if .Values.llmApiGateway.vault.vaultNamespace }}
vault.hashicorp.com/namespace: {{ .Values.llmApiGateway.vault.vaultNamespace }}
{{- end }}
{{- end }}
vault.hashicorp.com/agent-service-account-token-volume-name: vault-token
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}

{{/*
Generate all pod annotations
*/}}
{{- define "llm-api-gateway.podAnnotations" -}}
{{- $annotations := dict -}}

{{- if .Values.llmApiGateway.podAnnotations -}}
{{- $annotations = merge $annotations .Values.llmApiGateway.podAnnotations -}}
{{- end -}}

{{- if not (and .Values.llmApiGateway.vault .Values.llmApiGateway.vault.noVaultAnnotations) -}}
{{- $vaultAnnotations := include "llm-api-gateway.vaultAnnotations" . | fromYaml -}}
{{- if $vaultAnnotations -}}
{{- $annotations = merge $annotations $vaultAnnotations -}}
{{- end -}}
{{- end -}}

{{- toYaml $annotations -}}
{{- end }}
