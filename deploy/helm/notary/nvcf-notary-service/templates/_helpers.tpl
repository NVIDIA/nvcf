{{/*
Expand the name of the chart.
*/}}
{{- define "nvcf-notary-service.name" -}}
{{- default .Chart.Name .Values.notary.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-notary-service.fullname" -}}
{{- if .Values.notary.fullnameOverride }}
{{- .Values.notary.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.notary.nameOverride }}
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
{{- define "nvcf-notary-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "nvcf-notary-service.namespace" -}}
{{- default .Release.Namespace .Values.notary.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "nvcf-notary-service.image" -}}
{{- $registry := required "A valid image registry (.Values.notary.image.registry) is required!" .Values.notary.image.registry -}}
{{- $repository := required "A valid image repository (.Values.notary.image.repository) is required!" .Values.notary.image.repository -}}
{{- $tag := .Values.notary.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "nvcf-notary-service.labels" -}}
helm.sh/chart: {{ include "nvcf-notary-service.chart" . }}
{{ include "nvcf-notary-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-notary-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-notary-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}
{{- define "nvcf-notary-service.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "nvcf-notary"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl" 
vault.hashicorp.com/secret-volume-path: "/home/app/vault"
{{- end }}
