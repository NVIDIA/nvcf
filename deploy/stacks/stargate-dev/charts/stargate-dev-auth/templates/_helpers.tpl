{{/* SPDX-License-Identifier: Apache-2.0 */}}

{{- define "stargate-dev-auth.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- /* The stack installs this chart as release stargate-dev-auth, and the
router values name its Service and Secret after the release. */}}
{{- define "stargate-dev-auth.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "stargate-dev-auth.labels" -}}
app.kubernetes.io/name: {{ include "stargate-dev-auth.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: auth
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "stargate-dev-auth.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stargate-dev-auth.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
