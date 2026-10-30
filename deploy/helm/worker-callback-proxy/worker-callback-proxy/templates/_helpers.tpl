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

{{- define "workerCallbackProxy.name" -}}
{{- default "worker-callback-proxy" .Values.workerCallbackProxy.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "workerCallbackProxy.fullname" -}}
{{- if .Values.workerCallbackProxy.fullnameOverride -}}
{{- .Values.workerCallbackProxy.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "workerCallbackProxy.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "workerCallbackProxy.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | quote }}
{{ include "workerCallbackProxy.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
{{- end -}}

{{- define "workerCallbackProxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "workerCallbackProxy.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "workerCallbackProxy.image" -}}
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

{{- define "workerCallbackProxy.workerDomain" -}}
{{- $d := required "workerCallbackProxy.workerDomain is required, for example grpc-worker.example.com" .Values.workerCallbackProxy.workerDomain -}}
{{- $d = trimSuffix "." (trimPrefix "*." $d) -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$" $d) -}}
{{- fail (printf "workerCallbackProxy.workerDomain %q must be a lowercase DNS name with at least two labels" $d) -}}
{{- end -}}
{{- $d -}}
{{- end -}}

{{- define "workerCallbackProxy.serviceName" -}}
{{- default (include "workerCallbackProxy.fullname" .) .Values.workerCallbackProxy.service.name -}}
{{- end -}}

{{- define "workerCallbackProxy.frontendSecretName" -}}
{{- $f := .Values.workerCallbackProxy.tls.frontend -}}
{{- if $f.existingSecret -}}
{{- $f.existingSecret -}}
{{- else if $f.certificate.create -}}
{{- printf "%s-frontend-tls" (include "workerCallbackProxy.fullname" .) -}}
{{- else -}}
{{- fail "workerCallbackProxy.tls.frontend: set existingSecret or certificate.create" -}}
{{- end -}}
{{- end -}}

{{- define "workerCallbackProxy.grpcProxyServerName" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- default (printf "*.grpc-headless.%s.svc.%s" .Release.Namespace $c.clusterDomain) $c.grpcProxy.tlsServerName -}}
{{- end -}}

{{/*
"true" when a listener serves the *.<workerDomain> frontend certificate.
*/}}
{{- define "workerCallbackProxy.frontendTls" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- if or $c.grpcProxy.enabled (and $c.invocation.enabled $c.invocation.tls.enabled) -}}true{{- end -}}
{{- end -}}

{{- define "workerCallbackProxy.invocationAuthorityRegex" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- $ns := default .Release.Namespace $c.invocation.namespace -}}
{{- $host := printf "%s.%s.svc.%s" $c.invocation.serviceName $ns $c.clusterDomain -}}
{{- printf "^\\d+-\\d+-\\d+-\\d+\\.%s:%d$" (regexQuoteMeta $host) (int $c.invocation.port) -}}
{{- end -}}

{{/*
Fails the render on value combinations the schema cannot express.
*/}}
{{- define "workerCallbackProxy.validate" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- if not (or $c.grpcProxy.enabled $c.invocation.enabled) -}}
{{- fail "workerCallbackProxy: enable grpcProxy, invocation, or both" -}}
{{- end -}}
{{- $drain := int $c.drainTimeSeconds -}}
{{- $sleep := int $c.preStopSleepSeconds -}}
{{- $grace := int $c.terminationGracePeriodSeconds -}}
{{- if not (and (le $drain $sleep) (lt $sleep $grace)) -}}
{{- fail (printf "workerCallbackProxy: need drainTimeSeconds (%d) <= preStopSleepSeconds (%d) < terminationGracePeriodSeconds (%d)" $drain $sleep $grace) -}}
{{- end -}}
{{- $svc := $c.service -}}
{{- if and $svc.loadBalancerClass (ne $svc.type "LoadBalancer") -}}
{{- fail "workerCallbackProxy.service.loadBalancerClass requires service.type LoadBalancer" -}}
{{- end -}}
{{- if and $svc.externalTrafficPolicy (eq $svc.type "ClusterIP") -}}
{{- fail "workerCallbackProxy.service.externalTrafficPolicy requires service.type NodePort or LoadBalancer" -}}
{{- end -}}
{{- $ports := list (int $c.ports.health) (int $c.ports.metrics) (int $c.ports.admin) -}}
{{- if $c.invocation.enabled -}}
{{- $ports = append $ports (int $c.ports.invocationTcp) -}}
{{- end -}}
{{- if ne (len $ports) (len (uniq $ports)) -}}
{{- fail "workerCallbackProxy.ports: health, metrics, admin and invocationTcp must differ" -}}
{{- end -}}
{{- if and $c.metrics.serviceMonitor.enabled (not $c.metrics.service.enabled) -}}
{{- fail "workerCallbackProxy.metrics.serviceMonitor.enabled requires metrics.service.enabled" -}}
{{- end -}}
{{- $cert := $c.tls.frontend.certificate -}}
{{- if and (include "workerCallbackProxy.frontendTls" .) $cert.create (not $c.tls.frontend.existingSecret) -}}
{{- $_ := required "workerCallbackProxy.tls.frontend.certificate.issuerRef.name is required when certificate.create is true" $cert.issuerRef.name -}}
{{- end -}}
{{- end -}}
