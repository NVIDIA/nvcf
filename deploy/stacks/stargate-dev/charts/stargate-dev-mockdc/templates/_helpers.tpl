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
