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

{{- define "wcp.name" -}}
{{- default "worker-callback-proxy" .Values.workerCallbackProxy.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "wcp.fullname" -}}
{{- if .Values.workerCallbackProxy.fullnameOverride -}}
{{- .Values.workerCallbackProxy.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "wcp.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "wcp.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | quote }}
{{ include "wcp.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "wcp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "wcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "wcp.image" -}}
{{- $img := .Values.workerCallbackProxy.image -}}
{{- $registry := trimSuffix "/" $img.registry -}}
{{- $repository := required "workerCallbackProxy.image.repository is required" $img.repository -}}
{{- $tag := default (printf "v%s" .Chart.AppVersion) $img.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{- define "wcp.workerDomain" -}}
{{- $d := required "workerCallbackProxy.workerDomain is required, for example grpc-worker.example.com" .Values.workerCallbackProxy.workerDomain -}}
{{- $d = trimSuffix "." (trimPrefix "*." $d) -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$" $d) -}}
{{- fail (printf "workerCallbackProxy.workerDomain %q must be a lowercase DNS name with at least two labels" $d) -}}
{{- end -}}
{{- $d -}}
{{- end -}}

{{- define "wcp.serviceName" -}}
{{- default (include "wcp.fullname" .) .Values.workerCallbackProxy.service.name -}}
{{- end -}}

{{- define "wcp.frontendSecretName" -}}
{{- $f := .Values.workerCallbackProxy.tls.frontend -}}
{{- if $f.existingSecret -}}
{{- $f.existingSecret -}}
{{- else if $f.certificate.create -}}
{{- printf "%s-frontend-tls" (include "wcp.fullname" .) -}}
{{- else -}}
{{- fail "workerCallbackProxy.tls.frontend: set existingSecret or certificate.create" -}}
{{- end -}}
{{- end -}}

{{- define "wcp.grpcProxyServerName" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- default (printf "*.grpc-headless.%s.svc.%s" .Release.Namespace $c.clusterDomain) $c.grpcProxy.tlsServerName -}}
{{- end -}}

{{- define "wcp.invocationAuthorityRegex" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- $ns := default .Release.Namespace $c.invocation.namespace -}}
{{- $host := printf "%s.%s.svc.%s" $c.invocation.serviceName $ns $c.clusterDomain -}}
{{- printf "^\\d+-\\d+-\\d+-\\d+\\.%s:%d$" (regexQuoteMeta $host) (int $c.invocation.port) -}}
{{- end -}}
