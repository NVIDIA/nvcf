{{/* SPDX-License-Identifier: Apache-2.0 */}}

{{- define "stargate-dev-auth.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "stargate-dev-auth.fullname" -}}
{{- if contains (include "stargate-dev-auth.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "stargate-dev-auth.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
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

{{- define "stargate-dev-auth.image" -}}
{{- $repository := required "image.repository is required" .Values.image.repository -}}
{{- $digest := required "image.digest is required" .Values.image.digest -}}
{{- if not (regexMatch "^sha256:[a-f0-9]{64}$" $digest) -}}
{{- fail "image.digest must be a sha256 digest" -}}
{{- end -}}
{{- printf "%s@%s" $repository $digest -}}
{{- end -}}
