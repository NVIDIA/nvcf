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

{{- define "nvcf-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}

{{- define "nvcf-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}

{{- define "nvcf-gateway.labels" -}}
helm.sh/chart: {{ include "nvcf-gateway.chart" . }}
{{ include "nvcf-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}

{{- define "nvcf-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Validate that enabled HTTPRoutes do not compete for the same hostname and
root PathPrefix match on the shared Gateway. All HTTPRoute templates in this
chart currently route PathPrefix /, so duplicate hostnames are ambiguous.
*/}}
{{- define "nvcf-gateway.validateUniqueRootHTTPRouteHostnames" -}}
{{- if .Values.nvcfGatewayRoutes.enabled -}}
{{- $seenHostnames := dict -}}
{{- $httpRouteKeys := list "nvcfApi" "nvctApi" "apiKeys" "invocation" "llmApiGateway" "llmInvocation" "vanityGateway" "sis" "nvcfUi" -}}
{{- range $routeKey := $httpRouteKeys -}}
  {{- $route := index $.Values.nvcfGatewayRoutes.routes $routeKey -}}
  {{- if and $route $route.enabled -}}
    {{- $routeName := tpl (toString (default $routeKey $route.name)) $ -}}
    {{- range $rawHostname := default (list) $route.hostnames -}}
      {{- $hostname := tpl (toString $rawHostname) $ | lower | trimSuffix "." -}}
      {{- if hasKey $seenHostnames $hostname -}}
        {{- fail (printf "nvcfGatewayRoutes HTTPRoute host conflict: routes %s and %s both use hostname %q with PathPrefix /" (index $seenHostnames $hostname) $routeName $hostname) -}}
      {{- end -}}
      {{- $_ := set $seenHostnames $hostname $routeName -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- end -}}
{{- end }}
