{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

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

{{- if and (include "llm-api-gateway.vaultEnabled" .) (not (and .Values.llmApiGateway.vault .Values.llmApiGateway.vault.noVaultAnnotations)) -}}
{{- $vaultAnnotations := include "llm-api-gateway.vaultAnnotations" . | fromYaml -}}
{{- if $vaultAnnotations -}}
{{- $annotations = merge $annotations $vaultAnnotations -}}
{{- end -}}
{{- end -}}

{{- if $annotations -}}
{{- toYaml $annotations -}}
{{- end -}}
{{- end }}

{{/*
Vault Agent integration: annotations, the vault-token and vault-config-templates
volumes, and the agent template ConfigMap. Renders "true" or nothing.
*/}}
{{- define "llm-api-gateway.vaultEnabled" -}}
{{- if dig "vault" "enabled" true .Values.llmApiGateway -}}true{{- end -}}
{{- end }}

{{/*
Caller authentication mode: nvcf, staticKeys or anonymous. Fails on anything
else, so a typo cannot fall through to a mode nobody selected.
*/}}
{{- define "llm-api-gateway.authMode" -}}
{{- $mode := dig "auth" "mode" "nvcf" .Values.llmApiGateway | toString -}}
{{- if not (has $mode (list "nvcf" "staticKeys" "anonymous")) -}}
{{- fail (printf "llmApiGateway.auth.mode must be nvcf, staticKeys or anonymous, got %q" $mode) -}}
{{- end -}}
{{- $mode -}}
{{- end }}

{{- define "llm-api-gateway.apiKeysMountPath" -}}
/etc/llm-api-gateway/auth
{{- end }}

{{- define "llm-api-gateway.tlsMountPath" -}}
/etc/llm-api-gateway/tls
{{- end }}

{{- define "llm-api-gateway.tlsEnabled" -}}
{{- if dig "tls" "enabled" false .Values.llmApiGateway -}}true{{- end -}}
{{- end }}

{{/*
Reject auth and TLS settings the gateway would refuse at startup, or that would
start a gateway nobody can reach. Renders nothing.
*/}}
{{- define "llm-api-gateway.validateAuthTls" -}}
{{- $mode := include "llm-api-gateway.authMode" . -}}
{{- $auth := .Values.llmApiGateway.auth | default dict -}}
{{- if and (eq $mode "nvcf") (not (.Values.llmApiGateway.config.nvcfGrpcAddr | toString | trim)) -}}
{{- fail "llmApiGateway.config.nvcfGrpcAddr is required when llmApiGateway.auth.mode is nvcf; use auth.mode staticKeys or anonymous to run without the NVCF API" -}}
{{- end -}}
{{- if eq $mode "staticKeys" -}}
{{- if not (dig "staticKeys" "existingSecret" "" $auth | toString | trim) -}}
{{- fail "llmApiGateway.auth.staticKeys.existingSecret is required when llmApiGateway.auth.mode is staticKeys: name a Secret with key api-keys.json" -}}
{{- end -}}
{{- $paths := dig "staticKeys" "allowedPaths" (list) $auth -}}
{{- if not $paths -}}
{{- fail "llmApiGateway.auth.staticKeys.allowedPaths must list at least one path when llmApiGateway.auth.mode is staticKeys" -}}
{{- end -}}
{{- range $paths -}}
{{- if not (regexMatch "^/[^,[:space:]]*$" (toString .)) -}}
{{- fail (printf "llmApiGateway.auth.staticKeys.allowedPaths entries must start with / and contain no comma or whitespace, got %q" (toString .)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if dig "serviceToken" "existingSecret" "" $auth -}}
{{- if not (regexMatch "^[-._a-zA-Z0-9]+$" (dig "serviceToken" "key" "" $auth | toString)) -}}
{{- fail "llmApiGateway.auth.serviceToken.key must be a Secret key name when llmApiGateway.auth.serviceToken.existingSecret is set" -}}
{{- end -}}
{{- end -}}
{{- if and (include "llm-api-gateway.tlsEnabled" .) (not (dig "tls" "existingSecret" "" .Values.llmApiGateway | toString | trim)) -}}
{{- fail "llmApiGateway.tls.existingSecret is required when llmApiGateway.tls.enabled is true: name a kubernetes.io/tls Secret" -}}
{{- end -}}
{{- end }}

{{/*
Container env beyond the ConfigMap. Renders list items, or nothing.
*/}}
{{- define "llm-api-gateway.env" -}}
{{- $mode := include "llm-api-gateway.authMode" . -}}
{{- $auth := .Values.llmApiGateway.auth | default dict -}}
{{- if .Values.llmApiGateway.olric.enabled }}
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
{{- end }}
{{- if eq $mode "staticKeys" }}
- name: API_KEYS_PATH
  value: {{ printf "%s/api-keys.json" (include "llm-api-gateway.apiKeysMountPath" .) | quote }}
- name: STATIC_ALLOWED_PATHS
  value: {{ join "," (dig "staticKeys" "allowedPaths" (list) $auth) | quote }}
{{- else if eq $mode "anonymous" }}
- name: ALLOW_ANONYMOUS
  value: "true"
{{- end }}
{{- with dig "serviceToken" "existingSecret" "" $auth }}
- name: STARGATE_SERVICE_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ . | quote }}
      key: {{ dig "serviceToken" "key" "" $auth | quote }}
{{- end }}
{{- if include "llm-api-gateway.tlsEnabled" . }}
- name: TLS_CERT_FILE
  value: {{ printf "%s/tls.crt" (include "llm-api-gateway.tlsMountPath" .) | quote }}
- name: TLS_KEY_FILE
  value: {{ printf "%s/tls.key" (include "llm-api-gateway.tlsMountPath" .) | quote }}
{{- end }}
{{- end }}

{{/*
Container volume mounts. Renders list items, or nothing.
*/}}
{{- define "llm-api-gateway.volumeMounts" -}}
{{- if include "llm-api-gateway.vaultEnabled" . }}
- name: vault-config-templates
  mountPath: /vault/config/templates
  readOnly: true
{{- end }}
{{- if eq (include "llm-api-gateway.authMode" .) "staticKeys" }}
{{- /*
No subPath: Kubernetes swaps the projected files atomically on Secret updates,
and the gateway re-reads the key file.
*/}}
- name: api-keys
  mountPath: {{ include "llm-api-gateway.apiKeysMountPath" . }}
  readOnly: true
{{- end }}
{{- if include "llm-api-gateway.tlsEnabled" . }}
- name: tls
  mountPath: {{ include "llm-api-gateway.tlsMountPath" . }}
  readOnly: true
{{- end }}
{{- end }}

{{/*
Pod volumes. Renders list items, or nothing.
*/}}
{{- define "llm-api-gateway.volumes" -}}
{{- if include "llm-api-gateway.vaultEnabled" . }}
- name: vault-token
  projected:
    sources:
    - serviceAccountToken:
        path: token
        expirationSeconds: 3600
        audience: {{ include "llm-api-gateway.vaultAudience" . }}
- name: vault-config-templates
  configMap:
    name: {{ include "llm-api-gateway.fullname" . }}-vault-agent-tpl
    items:
      - key: secrets.json.tmpl
        path: secrets.json.tmpl
{{- end }}
{{- if eq (include "llm-api-gateway.authMode" .) "staticKeys" }}
- name: api-keys
  secret:
    secretName: {{ .Values.llmApiGateway.auth.staticKeys.existingSecret | toString | trim | quote }}
    items:
      - key: api-keys.json
        path: api-keys.json
{{- end }}
{{- if include "llm-api-gateway.tlsEnabled" . }}
- name: tls
  secret:
    secretName: {{ .Values.llmApiGateway.tls.existingSecret | toString | trim | quote }}
    items:
      - key: tls.crt
        path: tls.crt
      - key: tls.key
        path: tls.key
{{- end }}
{{- end }}

{{/*
Probe scheme. Renders a scheme line only for TLS, so the plaintext probes stay
as they were.
*/}}
{{- define "llm-api-gateway.probeScheme" -}}
{{- if include "llm-api-gateway.tlsEnabled" . -}}
scheme: HTTPS
{{- end -}}
{{- end }}
