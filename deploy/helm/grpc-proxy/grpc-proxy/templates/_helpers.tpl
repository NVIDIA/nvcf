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
HTTP/3 worker callback TLS Secret name.
*/}}
{{- define "grpc-proxy.http3SecretName" -}}
{{- $tls := dig "http3" "tls" dict .Values.grpcproxy -}}
{{- $certificate := dig "certificate" dict $tls -}}
{{- if dig "enabled" false $certificate -}}
{{- dig "secretName" "" $tls | default (printf "%s-worker-tls" (include "grpc-proxy.fullname" .)) -}}
{{- else -}}
{{- required "grpcproxy.http3.tls.secretName is required when grpcproxy.http3.enabled is true and grpcproxy.http3.tls.certificate.enabled is false" (dig "secretName" "" $tls) -}}
{{- end -}}
{{- end }}

{{/*
The one HTTP/3 callback base URL, used for SELF_WORKER_FQDN and the
Certificate. Each pod advertises https://<dashed-pod-ip>.<host>[:port], so the
URL must be https with a DNS host and no path. Without workerConnectBaseURL or
env.SELF_WORKER_FQDN, workers reach each pod through the grpc-headless Service.
*/}}
{{- define "grpc-proxy.http3CallbackURL" -}}
{{- $userEnv := .Values.grpcproxy.env | default dict -}}
{{- $configured := .Values.grpcproxy.workerConnectBaseURL | default "" | toString -}}
{{- $source := "grpcproxy.workerConnectBaseURL" -}}
{{- if hasKey $userEnv "SELF_WORKER_FQDN" -}}
{{- if $configured -}}
{{- fail "set either grpcproxy.workerConnectBaseURL or grpcproxy.env.SELF_WORKER_FQDN, not both" -}}
{{- end -}}
{{- $configured = get $userEnv "SELF_WORKER_FQDN" | default "" | toString -}}
{{- $source = "grpcproxy.env.SELF_WORKER_FQDN" -}}
{{- if not $configured -}}
{{- fail "grpcproxy.env.SELF_WORKER_FQDN is empty; remove it to use the grpc-headless default, or set an https callback URL" -}}
{{- end -}}
{{- end -}}
{{- $url := $configured | default (printf "https://grpc-headless.%s.svc.cluster.local:10084" (include "grpc-proxy.namespace" .)) -}}
{{- $parsed := urlParse $url -}}
{{- $invalid := "" -}}
{{- if ne $parsed.scheme "https" -}}
{{- $invalid = "it must use https, since grpcproxy.http3.enabled turns HTTP/1 callbacks off" -}}
{{- else if not $parsed.hostname -}}
{{- $invalid = "it has no host" -}}
{{- else if regexMatch "^[0-9.]+$|:" $parsed.hostname -}}
{{- $invalid = "its host is an IP address; each pod prepends its dashed IP to the host, so it must be a DNS name" -}}
{{- else if or (and $parsed.path (ne $parsed.path "/")) $parsed.query $parsed.fragment $parsed.userinfo -}}
{{- $invalid = "it must not have a path, query, fragment or user info" -}}
{{- end -}}
{{- if $invalid -}}
{{- if $configured -}}
{{- fail (printf "%s %q is not a valid HTTP/3 callback URL: %s" $source $url $invalid) -}}
{{- end -}}
{{- fail (printf "default HTTP/3 callback URL %q is invalid: %s" $url $invalid) -}}
{{- end -}}
{{- $url -}}
{{- end }}

{{/*
Certificate DNS names. The advertised names are <dashed-pod-ip>.<host>, so the
Certificate must include *.<host>; explicit dnsNames that lack it fail.
*/}}
{{- define "grpc-proxy.http3DnsNames" -}}
{{- $host := (urlParse (include "grpc-proxy.http3CallbackURL" .)).hostname -}}
{{- $wildcard := printf "*.%s" $host -}}
{{- $dnsNames := dig "http3" "tls" "certificate" "dnsNames" list .Values.grpcproxy -}}
{{- if not $dnsNames -}}
{{- $dnsNames = list $wildcard -}}
{{- else if not (has $wildcard $dnsNames) -}}
{{- fail (printf "grpcproxy.http3.tls.certificate.dnsNames must include %q, the per-pod names advertised for the HTTP/3 callback URL" $wildcard) -}}
{{- end -}}
{{- toJson $dnsNames -}}
{{- end }}

{{/*
Environment owned by grpcproxy.http3, as a YAML map. Empty when HTTP/3 is off.
*/}}
{{- define "grpc-proxy.http3Env" -}}
{{- if dig "http3" "enabled" false .Values.grpcproxy -}}
{{- $userEnv := .Values.grpcproxy.env | default dict -}}
{{- range $key := list "PUBLIC_CERT_PATH" "PRIVATE_KEY_PATH" -}}
{{- if hasKey $userEnv $key -}}
{{- fail (printf "grpcproxy.env.%s cannot be set when grpcproxy.http3.enabled is true; the chart mounts the TLS Secret and sets it" $key) -}}
{{- end -}}
{{- end -}}
{{- $env := dict
  "ENABLE_HTTP3_CONNECT" "true"
  "ENABLE_HTTP1_CONNECT" "false"
  "PUBLIC_CERT_PATH" "/etc/grpc-proxy-tls/tls.crt"
  "PRIVATE_KEY_PATH" "/etc/grpc-proxy-tls/tls.key"
  "SELF_WORKER_FQDN" (include "grpc-proxy.http3CallbackURL" .) -}}
{{- toYaml $env -}}
{{- end -}}
{{- end }}

{{/*
Container and Service ports as a JSON list. HTTP/3 adds UDP 10084 unless
grpcproxy.ports already lists it.
*/}}
{{- define "grpc-proxy.ports" -}}
{{- $ports := .Values.grpcproxy.ports -}}
{{- if dig "http3" "enabled" false .Values.grpcproxy -}}
{{- $listed := false -}}
{{- range $ports -}}
{{- if and (eq (int .containerPort) 10084) (eq (upper (toString .protocol)) "UDP") -}}
{{- $listed = true -}}
{{- end -}}
{{- end -}}
{{- if not $listed -}}
{{- $ports = append $ports (dict "name" "worker-quic" "containerPort" 10084 "protocol" "UDP") -}}
{{- end -}}
{{- end -}}
{{- toJson $ports -}}
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
