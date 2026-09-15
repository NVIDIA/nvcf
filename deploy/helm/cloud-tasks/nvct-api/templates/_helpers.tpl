{{/*
Expand the name of the chart.
*/}}
{{- define "nvct-api.name" -}}
{{- default .Chart.Name .Values.nvctApi.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvct-api.fullname" -}}
{{- if .Values.nvctApi.fullnameOverride }}
{{- .Values.nvctApi.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nvctApi.nameOverride }}
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
{{- define "nvct-api.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "nvct-api.namespace" -}}
{{- default .Release.Namespace .Values.nvctApi.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "nvct-api.image" -}}
{{- $registry := required "A valid image registry (.Values.nvctApi.image.registry) is required!" .Values.nvctApi.image.registry -}}
{{- $repository := required "A valid image repository (.Values.nvctApi.image.repository) is required!" .Values.nvctApi.image.repository -}}
{{- $tag := .Values.nvctApi.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "nvct-api.labels" -}}
helm.sh/chart: {{ include "nvct-api.chart" . }}
{{ include "nvct-api.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvct-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvct-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Remote config ConfigMap name. Default: <fullname>-remote-config (release-scoped
to avoid collisions across installs in the same namespace). Override via
.Values.nvctApi.remoteConfig.configMapName when a fixed external name is required.
*/}}
{{- define "nvct-api.remoteConfigName" -}}
{{- .Values.nvctApi.remoteConfig.configMapName | default (printf "%s-remote-config" (include "nvct-api.fullname" .)) -}}
{{- end -}}

{{/*
Remote config Role/RoleBinding name. Release-scoped via the fullname helper.
*/}}
{{- define "nvct-api.remoteConfigReaderName" -}}
{{- printf "%s-remote-config-reader" (include "nvct-api.fullname" .) -}}
{{- end -}}

{{/*
Vault Agent Injector Annotations for JWT Auth
Bound OpenBao JWT role and KSA name: nvct-api (see nvcf-openbao-migrations migrations/20_setup_nvct.sh).
*/}}
{{- define "nvct-api.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "nvct-api"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/auth-config-token-path: "/var/run/secrets/openbao/token"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/home/app/vault"
vault.hashicorp.com/template-static-secret-render-interval: "1h"
{{- end }}
