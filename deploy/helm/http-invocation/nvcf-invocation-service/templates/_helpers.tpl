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
{{- define "nvcf-invocation-service.name" -}}
{{- default .Chart.Name .Values.invocation.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-invocation-service.fullname" -}}
{{- if .Values.invocation.fullnameOverride }}
{{- .Values.invocation.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.invocation.nameOverride }}
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
{{- define "nvcf-invocation-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "nvcf-invocation-service.namespace" -}}
{{- default .Release.Namespace .Values.invocation.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "nvcf-invocation-service.image" -}}
{{- $registry := required "A valid image registry (.Values.invocation.image.registry) is required!" .Values.invocation.image.registry -}}
{{- $repository := required "A valid image repository (.Values.invocation.image.repository) is required!" .Values.invocation.image.repository -}}
{{- $tag := .Values.invocation.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "nvcf-invocation-service.labels" -}}
helm.sh/chart: {{ include "nvcf-invocation-service.chart" . }}
{{ include "nvcf-invocation-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-invocation-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-invocation-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "nvcf-invocation-service.serviceAccountName" -}}
{{- if .Values.invocation.serviceAccount.create }}
{{- default (include "nvcf-invocation-service.fullname" .) .Values.invocation.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.invocation.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the Docker config JSON for image pull secret
*/}}
{{- define "imagePullSecret" }}
{{- with .Values.invocation.registry }}
{{- printf "{\"auths\":{\"%s\":{\"username\":\"%s\",\"password\":\"%s\",\"email\":\"%s\",\"auth\":\"%s\"}}}" .server .username .password .email (printf "%s:%s" .username .password | b64enc) | b64enc }}
{{- end }}
{{- end }}

{{/*
Secret mounted for the worker TLS listener.
*/}}
{{- define "nvcf-invocation-service.workerTlsSecretName" -}}
{{- $tls := .Values.invocation.workerTls.tls -}}
{{- if $tls.certificate.enabled -}}
{{- default (printf "%s-worker-tls" (include "nvcf-invocation-service.fullname" .)) $tls.secretName -}}
{{- else -}}
{{- required "invocation.workerTls.tls.secretName is required when invocation.workerTls.enabled is true and invocation.workerTls.tls.certificate.enabled is false" $tls.secretName -}}
{{- end -}}
{{- end -}}

{{/*
Service name suffix of the per-pod callback names.
*/}}
{{- define "nvcf-invocation-service.serviceHost" -}}
{{- printf "invocation.%s.svc.cluster.local" (include "nvcf-invocation-service.namespace" .) -}}
{{- end -}}

{{/*
Environment owned by invocation.workerTls, as a YAML map. Empty when it is off.
*/}}
{{- define "nvcf-invocation-service.workerTlsEnv" -}}
{{- $w := .Values.invocation.workerTls -}}
{{- if $w.enabled -}}
WORKER_TLS__ENABLED: "true"
WORKER_TLS__PORT: {{ $w.port | toString | quote }}
WORKER_TLS__CERT_PATH: "/etc/invocation-service/worker-tls/tls.crt"
WORKER_TLS__KEY_PATH: "/etc/invocation-service/worker-tls/tls.key"
{{- end -}}
{{- end -}}

{{/*
Whether the env ConfigMap is rendered and loaded.
*/}}
{{- define "nvcf-invocation-service.hasEnvConfig" -}}
{{- if or .Values.invocation.env .Values.invocation.metrics.enabled .Values.invocation.cors.allowedOrigins .Values.invocation.polling.enforceOwnership .Values.invocation.workerTls.enabled -}}
true
{{- end -}}
{{- end -}}

{{/*
Checksum of the values rendered into the env ConfigMap. workerTls joins the
input only when enabled, so existing releases keep their checksum.
*/}}
{{- define "nvcf-invocation-service.configEnvChecksum" -}}
{{- $inputs := dict "env" .Values.invocation.env "metrics" .Values.invocation.metrics "tracing" .Values.invocation.tracing "cors" .Values.invocation.cors "polling" .Values.invocation.polling -}}
{{- if .Values.invocation.workerTls.enabled -}}
{{- $_ := set $inputs "workerTls" .Values.invocation.workerTls -}}
{{- end -}}
{{- $inputs | toYaml | sha256sum -}}
{{- end -}}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}
{{- define "nvcf-invocation-service.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "invocation-api"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl" 
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}
