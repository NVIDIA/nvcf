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
{{- define "grpc-proxy.name" -}}
{{- default .Chart.Name .Values.grpcproxy.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "grpc-proxy.fullname" -}}
{{- if .Values.grpcproxy.fullnameOverride }}
{{- .Values.grpcproxy.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.grpcproxy.nameOverride }}
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
{{- define "grpc-proxy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "grpc-proxy.namespace" -}}
{{- default .Release.Namespace .Values.grpcproxy.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "grpc-proxy.image" -}}
{{- $registry := required "A valid image registry (.Values.grpcproxy.image.registry) is required!" .Values.grpcproxy.image.registry -}}
{{- $repository := required "A valid image repository (.Values.grpcproxy.image.repository) is required!" .Values.grpcproxy.image.repository -}}
{{- $tag := .Values.grpcproxy.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "grpc-proxy.labels" -}}
helm.sh/chart: {{ include "grpc-proxy.chart" . }}
{{ include "grpc-proxy.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "grpc-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "grpc-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the Docker config JSON for image pull secret
*/}}
{{- define "imagePullSecret" }}
{{- with .Values.grpcproxy.registry }}
{{- printf "{\"auths\":{\"%s\":{\"username\":\"%s\",\"password\":\"%s\",\"email\":\"%s\",\"auth\":\"%s\"}}}" .server .username .password .email (printf "%s:%s" .username .password | b64enc) | b64enc }}
{{- end }}
{{- end }}

{{- define "grpc-proxy.templatePath" -}}
/vault/configs/secrets.json.tmpl
{{- end }}

{{/*
Vault Annotations - return as YAML dict
*/}}
{{- define "grpc-proxy.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ if .Values.grpcproxy.vault }}{{ .Values.grpcproxy.vault.vaultRole | default "grpc-proxy-proxy" }}{{ else }}"grpc-proxy-proxy"{{ end }}
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
{{- if .Values.grpcproxy.vault }}
{{- if .Values.grpcproxy.vault.jwtAuthPath }}
vault.hashicorp.com/jwt-auth-path: {{ .Values.grpcproxy.vault.jwtAuthPath }}
{{- end }}
{{- if .Values.grpcproxy.vault.vaultAddress }}
vault.hashicorp.com/service: {{ .Values.grpcproxy.vault.vaultAddress }}
{{- end }}
{{- if .Values.grpcproxy.vault.vaultNamespace }}
vault.hashicorp.com/namespace: {{ .Values.grpcproxy.vault.vaultNamespace }}
{{- end }}
{{- if and .Values.grpcproxy.vault.secretsTemplateSource (eq .Values.grpcproxy.vault.secretsTemplateSource "inline") }}
vault.hashicorp.com/agent-inject-template-file-secrets.json: {{ .Values.grpcproxy.vault.secretsTemplate | quote }}
{{- else }}
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
{{- end }}
{{- else }}
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
{{- end }}
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}

{{/*
Generate all pod annotations in one place
*/}}
{{- define "grpc-proxy.podAnnotations" -}}
{{- $annotations := dict -}}

{{/* User-defined pod annotations */}}
{{- if .Values.grpcproxy.podAnnotations -}}
{{- $annotations = merge $annotations .Values.grpcproxy.podAnnotations -}}
{{- end -}}

{{/* Environment checksum annotation */}}
{{- if .Values.grpcproxy.env -}}
{{- $_ := set $annotations "checksum/config-env" (toYaml .Values.grpcproxy.env | sha256sum) -}}
{{- end -}}

{{/* Vault annotations (included by default unless explicitly disabled) */}}
{{- if not (and .Values.grpcproxy.vault .Values.grpcproxy.vault.noVaultAnnotations) -}}
{{- $vaultAnnotations := include "grpc-proxy.vaultAnnotations" . | fromYaml -}}
{{- if $vaultAnnotations -}}
{{- $annotations = merge $annotations $vaultAnnotations -}}
{{- end -}}
{{- end -}}

{{/* Output the merged annotations */}}
{{- toYaml $annotations -}}
{{- end }}

{{/*
Validate deploymentType value
*/}}
{{- define "grpc-proxy.validateDeploymentType" -}}
{{- $validTypes := list "deployment" "daemonset" -}}
{{- if not (has .Values.grpcproxy.deploymentType $validTypes) -}}
{{- fail (printf "Invalid deploymentType '%s'. Must be one of: %s" .Values.grpcproxy.deploymentType (join ", " $validTypes)) -}}
{{- end -}}
{{- end }}
