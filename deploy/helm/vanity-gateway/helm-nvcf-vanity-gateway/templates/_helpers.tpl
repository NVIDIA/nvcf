{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{- define "vanity-gateway.name" -}}
{{- default .Chart.Name .Values.vanityGateway.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "vanity-gateway.fullname" -}}
{{- if .Values.vanityGateway.fullnameOverride -}}
{{- .Values.vanityGateway.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.vanityGateway.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "vanity-gateway.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | quote }}
{{ include "vanity-gateway.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "vanity-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vanity-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "vanity-gateway.serviceAccountName" -}}
{{- if .Values.vanityGateway.serviceAccount.create -}}
{{- default (include "vanity-gateway.fullname" .) .Values.vanityGateway.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.vanityGateway.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "vanity-gateway.image" -}}
{{- $registry := trimSuffix "/" .Values.vanityGateway.image.registry -}}
{{- $repository := required "A valid image repository (.Values.vanityGateway.image.repository) is required!" .Values.vanityGateway.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.vanityGateway.image.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}
