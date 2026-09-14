{{/*
Expand the name of the chart.
*/}}
{{- define "sis.name" -}}
{{- default .Chart.Name .Values.sis.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "sis.fullname" -}}
{{- if .Values.sis.fullnameOverride }}
{{- .Values.sis.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.sis.nameOverride }}
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
{{- define "sis.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "sis.namespace" -}}
{{- default .Release.Namespace .Values.sis.namespace -}}
{{- end -}}

{{/*
Derive the full image value
This supports using a local image and a remote repository, with an optional
immutable digest pin that takes precedence over tag.

For local:
values:
    image:
      name: <local-image-name>
      tag: <tag>

For remote (tag):
values:
    image:
      registry: <registry>
      repository: <namespace>/<image>
      tag: <tag>

For remote (digest, preferred for production):
values:
    image:
      registry: <registry>
      repository: <namespace>/<image>
      digest: sha256:<...>
*/}}
{{- define "sis.image" -}}
{{- if .Values.sis.image.name }}
{{- if .Values.sis.image.digest -}}
{{- printf "%s@%s" .Values.sis.image.name .Values.sis.image.digest -}}
{{- else -}}
{{- $tag := required "Image reference required: set .Values.sis.image.digest, .Values.sis.image.tag, or .Chart.AppVersion." (.Values.sis.image.tag | default .Chart.AppVersion) -}}
{{- printf "%s:%s" .Values.sis.image.name $tag -}}
{{- end -}}
{{- else }}
{{- $registry := required "A valid image registry (.Values.sis.image.registry) is required!" .Values.sis.image.registry -}}
{{- $repository := required "A valid image repository (.Values.sis.image.repository) is required!" .Values.sis.image.repository -}}
{{- if .Values.sis.image.digest -}}
{{- printf "%s/%s@%s" $registry $repository .Values.sis.image.digest -}}
{{- else -}}
{{- $tag := required "Image reference required: set .Values.sis.image.digest, .Values.sis.image.tag, or .Chart.AppVersion." (.Values.sis.image.tag | default .Chart.AppVersion) -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "sis.labels" -}}
helm.sh/chart: {{ include "sis.chart" . }}
{{ include "sis.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sis.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
OpenBao token for vault-agent auth. The chart owns the volume and mount (in
deployment.yaml) and points the injector at them via the two annotations below.

Keep "serviceaccount" in the mount path. The injector copies the app container's
mounts into vault-agent but skips any path containing "serviceaccount"; since it
also mounts this token itself, without that segment the token lands twice and the
pod is rejected ("mountPath must be unique").
*/}}
{{- define "sis.openbaoTokenVolumeName" -}}
openbao-token
{{- end -}}

{{- define "sis.openbaoTokenMountPath" -}}
/var/run/secrets/openbao/serviceaccount
{{- end -}}

{{- define "sis.openbaoTokenFileName" -}}
token
{{- end -}}

{{- define "sis.openbaoTokenPath" -}}
{{- printf "%s/%s" (include "sis.openbaoTokenMountPath" .) (include "sis.openbaoTokenFileName" .) -}}
{{- end -}}

{{/*
Remote-config resource names, release-scoped by default:
<fullname>-remote-config unless sis.remoteConfig.configMapName pins a literal.
*/}}
{{- define "sis.remoteConfigName" -}}
{{- default (printf "%s-remote-config" (include "sis.fullname" .)) .Values.sis.remoteConfig.configMapName -}}
{{- end -}}

{{- define "sis.remoteConfigReaderName" -}}
{{- printf "%s-remote-config-reader" (include "sis.fullname" .) -}}
{{- end -}}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}
{{- define "sis.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "sis-api"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-service-account-token-volume-name: {{ include "sis.openbaoTokenVolumeName" . | quote }}
vault.hashicorp.com/auth-config-token-path: {{ include "sis.openbaoTokenPath" . | quote }}
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/secret-volume-path: "/home/tomcat/vault-agent/secrets"
vault.hashicorp.com/agent-inject-secret-vault-secrets.json: "services/all/kv/data/nvcf/sis"
vault.hashicorp.com/agent-inject-template-file-vault-secrets.json: "/vault/config/templates/secrets.json.tmpl"
{{- end }}

{{/*
Derive the full image value
Expects a dictionary with 'image' and 'name' keys
An optional immutable digest pin takes precedence over tag.
Usage: {{ include "sis.image.full" (dict "image" .Values.sis.lls.hmacRotation.image "name" "sis.lls.hmacRotation.image") }}
*/}}
{{- define "sis.image.full" -}}
{{- $image := .image -}}
{{- $name := .name -}}
{{- $registry := required (printf "A valid image registry is required for %s.registry" $name) $image.registry -}}
{{- $repository := required (printf "A valid image repository is required for %s.repository" $name) $image.repository -}}
{{- if $image.digest -}}
{{- printf "%s/%s@%s" $registry $repository $image.digest -}}
{{- else -}}
{{- $tag := required (printf "A valid image tag is required for %s.tag" $name) $image.tag -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Resolve imagePullSecrets: prefer the list form (.Values.sis.imagePullSecrets),
fall back to the singular convenience field (.Values.sis.imagePullSecret.name).
Returns a YAML list of {name: ...} objects or nothing.
*/}}
{{- define "sis.imagePullSecrets" -}}
{{- if .Values.sis.imagePullSecrets -}}
{{- toYaml .Values.sis.imagePullSecrets -}}
{{- else if and .Values.sis.imagePullSecret .Values.sis.imagePullSecret.name -}}
- name: {{ .Values.sis.imagePullSecret.name }}
{{- end -}}
{{- end -}}
