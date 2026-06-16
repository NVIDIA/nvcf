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

{{- define "llm-request-router.name" -}}
{{- default .Chart.Name .Values.llmRequestRouter.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "llm-request-router.fullname" -}}
{{- if .Values.llmRequestRouter.fullnameOverride }}
{{- .Values.llmRequestRouter.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- include "llm-request-router.name" . }}
{{- end }}
{{- end }}

{{- define "llm-request-router.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "llm-request-router.labels" -}}
helm.sh/chart: {{ include "llm-request-router.chart" . }}
{{ include "llm-request-router.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "llm-request-router.selectorLabels" -}}
app.kubernetes.io/name: {{ include "llm-request-router.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "llm-request-router.selectorLabelSelector" -}}
{{- printf "app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s" (include "llm-request-router.name" .) .Release.Name -}}
{{- end }}

{{- define "llm-request-router.namespace" -}}
{{- default .Release.Namespace .Values.llmRequestRouter.namespace -}}
{{- end -}}

{{- define "llm-request-router.serviceAccountName" -}}
{{- if .Values.llmRequestRouter.serviceAccount.create }}
{{- default (include "llm-request-router.fullname" .) .Values.llmRequestRouter.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.llmRequestRouter.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "llm-request-router.image" -}}
{{- $registry := .Values.llmRequestRouter.image.registry -}}
{{- $repository := .Values.llmRequestRouter.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.llmRequestRouter.image.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.pkiMigrationsImage" -}}
{{- $img := .Values.llmRequestRouter.pki.image -}}
{{- $registry := $img.registry -}}
{{- $repository := required "llmRequestRouter.pki.image.repository is required when llmRequestRouter.pki.enabled is true" $img.repository -}}
{{- $tag := required "llmRequestRouter.pki.image.tag is required when llmRequestRouter.pki.enabled is true" $img.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.tlsSecretName" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- $certificate := .Values.llmRequestRouter.certificate | default dict -}}
{{- if $tls.secretName -}}
{{- $tls.secretName -}}
{{- else if $certificate.secretName -}}
{{- $certificate.secretName -}}
{{- else if $certificate.enabled -}}
{{- printf "%s-quic-tls" (include "llm-request-router.fullname" .) -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.tlsMountPath" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- if $tls.mountPath -}}
{{- $tls.mountPath -}}
{{- else if $tls.certPath -}}
{{- dir $tls.certPath -}}
{{- else -}}
/etc/stargate/tls
{{- end -}}
{{- end }}

{{/*
Vault Annotations
*/}}
{{- define "llm-request-router.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ if .Values.llmRequestRouter.vault }}{{ .Values.llmRequestRouter.vault.vaultRole | default "llm-request-router" }}{{ else }}"llm-request-router"{{ end }}
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
{{- if .Values.llmRequestRouter.vault }}
{{- if .Values.llmRequestRouter.vault.jwtAuthPath }}
vault.hashicorp.com/jwt-auth-path: {{ .Values.llmRequestRouter.vault.jwtAuthPath }}
{{- end }}
{{- if .Values.llmRequestRouter.vault.vaultAddress }}
vault.hashicorp.com/service: {{ .Values.llmRequestRouter.vault.vaultAddress }}
{{- end }}
{{- if .Values.llmRequestRouter.vault.vaultNamespace }}
vault.hashicorp.com/namespace: {{ .Values.llmRequestRouter.vault.vaultNamespace }}
{{- end }}
{{- end }}
vault.hashicorp.com/agent-service-account-token-volume-name: vault-token
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}

{{/*
Generate all pod annotations
*/}}
{{- define "llm-request-router.podAnnotations" -}}
{{- $annotations := dict -}}

{{- if .Values.llmRequestRouter.podAnnotations -}}
{{- $annotations = merge $annotations .Values.llmRequestRouter.podAnnotations -}}
{{- end -}}

{{- if not (and .Values.llmRequestRouter.vault .Values.llmRequestRouter.vault.noVaultAnnotations) -}}
{{- $vaultAnnotations := include "llm-request-router.vaultAnnotations" . | fromYaml -}}
{{- if $vaultAnnotations -}}
{{- $annotations = merge $annotations $vaultAnnotations -}}
{{- end -}}
{{- end -}}

{{- toYaml $annotations -}}
{{- end }}
