{{/*
Expand the name of the chart.
*/}}
{{- define "ess-api.name" -}}
{{- default .Chart.Name .Values.ess.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "ess-api.fullname" -}}
{{- if .Values.ess.fullnameOverride }}
{{- .Values.ess.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.ess.nameOverride }}
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
{{- define "ess-api.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "ess-api.namespace" -}}
{{- default .Release.Namespace .Values.ess.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "ess-api.image" -}}
{{- $registry := required "A valid image registry (.Values.ess.image.registry) is required!" .Values.ess.image.registry -}}
{{- $repository := required "A valid image repository (.Values.ess.image.repository) is required!" .Values.ess.image.repository -}}
{{- $tag := .Values.ess.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "ess-api.labels" -}}
helm.sh/chart: {{ include "ess-api.chart" . }}
{{ include "ess-api.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "ess-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ess-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}
{{- define "ess-api.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "ess-api"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-service-account-token-volume-name: "token"
vault.hashicorp.com/auth-config-token-path: "/var/run/secrets/kubernetes.io/serviceaccount/token"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl" 
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}
