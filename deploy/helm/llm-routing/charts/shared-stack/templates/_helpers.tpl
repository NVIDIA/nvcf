{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{- define "llm-shared.labels" -}}
app.kubernetes.io/name: llm-shared-stack
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "llm-shared.owned" -}}
{{- $annotations := .resource.metadata.annotations | default dict -}}
{{- if or (ne (index $annotations "meta.helm.sh/release-name") .root.Release.Name) (ne (index $annotations "meta.helm.sh/release-namespace") .root.Release.Namespace) -}}
{{- fail (printf "shared-stack: %s is not owned by this Helm release; preserve it and migrate its ownership before continuing" .resource.metadata.name) -}}
{{- end -}}
{{- end -}}

{{/* A missing installed key must never silently become a new credential. */}}
{{- define "llm-shared.token" -}}
{{- $secret := lookup "v1" "Secret" .root.Release.Namespace .name -}}
{{- if $secret -}}
{{- if .create -}}{{- include "llm-shared.owned" (dict "resource" $secret "root" .root) -}}{{- end -}}
{{- $value := index ($secret.data | default dict) .key | default "" -}}
{{- if not $value -}}{{- fail (printf "shared-stack: Secret %s is missing %s" .name .key) -}}{{- end -}}
{{- if not ($value | b64dec | trim) -}}{{- fail (printf "shared-stack: Secret %s contains an empty credential" .name) -}}{{- end -}}
{{- $value -}}
{{- else if or (not .create) .root.Release.IsUpgrade -}}
{{- fail (printf "shared-stack: required Secret %s is missing; restore it before continuing" .name) -}}
{{- else -}}
{{- randAlphaNum 48 | b64enc -}}
{{- end -}}
{{- end -}}
