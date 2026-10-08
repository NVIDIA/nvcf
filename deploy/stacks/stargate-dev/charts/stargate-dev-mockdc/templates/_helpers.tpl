{{/* SPDX-License-Identifier: Apache-2.0 */}}

{{- define "stargate-dev-mockdc.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "stargate-dev-mockdc.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "stargate-dev-mockdc.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "stargate-dev-mockdc.labels" -}}
app.kubernetes.io/name: {{ include "stargate-dev-mockdc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: mock-backend
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "stargate-dev-mockdc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stargate-dev-mockdc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "stargate-dev-mockdc.mockDynamoImage" -}}
{{- $repository := required "images.mockDynamo.repository is required" .Values.images.mockDynamo.repository -}}
{{- $digest := required "images.mockDynamo.digest is required" .Values.images.mockDynamo.digest -}}
{{- printf "%s@%s" $repository $digest -}}
{{- end -}}

{{- define "stargate-dev-mockdc.pylonImage" -}}
{{- $repository := required "images.pylon.repository is required" .Values.images.pylon.repository -}}
{{- $digest := required "images.pylon.digest is required" .Values.images.pylon.digest -}}
{{- printf "%s@%s" $repository $digest -}}
{{- end -}}
