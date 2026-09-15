{{/*
Expand the name of the chart.
*/}}
{{- define "nvcf-api.name" -}}
{{- default .Chart.Name .Values.api.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-api.fullname" -}}
{{- if .Values.api.fullnameOverride }}
{{- .Values.api.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.api.nameOverride }}
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
{{- define "nvcf-api.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "nvcf-api.namespace" -}}
{{- default .Release.Namespace .Values.api.namespace -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "nvcf-api.image" -}}
{{- $registry := required "A valid image registry (.Values.api.image.registry) is required!" .Values.api.image.registry -}}
{{- $repository := required "A valid image repository (.Values.api.image.repository) is required!" .Values.api.image.repository -}}
{{- $tag := .Values.api.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Derive the full image value
*/}}
{{- define "nvcf-api.accountBootstrapImage" -}}
{{- $registry := required "A valid image registry (.Values.api.accountBootstrap.image.registry) is required!" .Values.api.accountBootstrap.image.registry -}}
{{- $repository := required "A valid image repository (.Values.api.accountBootstrap.image.repository) is required!" .Values.api.accountBootstrap.image.repository -}}
{{- $tag := .Values.api.accountBootstrap.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "nvcf-api.labels" -}}
helm.sh/chart: {{ include "nvcf-api.chart" . }}
{{ include "nvcf-api.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the Docker config JSON for image pull secret
*/}}
{{- define "imagePullSecret" }}
{{- with .Values.api.registry }}
{{- printf "{\"auths\":{\"%s\":{\"username\":\"%s\",\"password\":\"%s\",\"email\":\"%s\",\"auth\":\"%s\"}}}" .server .username .password .email (printf "%s:%s" .username .password | b64enc) | b64enc }}
{{- end }}
{{- end }}

{{/*
Remote config ConfigMap name. Default: <fullname>-remote-config (release-scoped
to avoid collisions across installs in the same namespace). Override via
.Values.api.remoteConfig.configMapName when a fixed external name is required.
*/}}
{{- define "nvcf-api.remoteConfigName" -}}
{{- .Values.api.remoteConfig.configMapName | default (printf "%s-remote-config" (include "nvcf-api.fullname" .)) -}}
{{- end -}}

{{/*
Remote config Role/RoleBinding name. Release-scoped via the fullname helper.
*/}}
{{- define "nvcf-api.remoteConfigReaderName" -}}
{{- printf "%s-remote-config-reader" (include "nvcf-api.fullname" .) -}}
{{- end -}}

{{/*
Release-artifact annotations for worker sidecar images.

The remote-config sidecar image references embed Spring placeholders
(${nvcf.sidecars.hostname}/${nvcf.sidecars.repository}) that are resolved at
runtime from the NVCF_SIDECARS_HOSTNAME / NVCF_SIDECARS_REPOSITORY env vars.
Stack release-artifact tooling scans rendered manifests for concrete image
references and skips any value containing "${", so those worker images would
otherwise be omitted from stack release artifacts. This helper emits a fully
resolved "<hostname>/<repository>/<image>:<tag>" reference for every sidecar
entry that carries the placeholder prefix, mirroring the release-artifact-*-image
annotations rendered by nvca-operator's self-managed-nvcfbackend-cm.yaml.

Nothing is emitted unless both NVCF_SIDECARS_HOSTNAME and NVCF_SIDECARS_REPOSITORY
are set, so partially-resolved (and therefore invalid) references are never
produced.
*/}}
{{- define "nvcf-api.sidecarReleaseArtifacts" -}}
{{- $env := .Values.api.env | default dict -}}
{{- $hostname := $env.NVCF_SIDECARS_HOSTNAME | default "" -}}
{{- $repository := $env.NVCF_SIDECARS_REPOSITORY | default "" -}}
{{- $remoteConfig := .Values.api.remoteConfig | default dict -}}
{{- $configData := $remoteConfig.configData | default dict -}}
{{- $nvcf := $configData.nvcf | default dict -}}
{{- $sidecars := $nvcf.sidecars | default dict -}}
{{- if and $hostname $repository -}}
{{- range $key, $value := $sidecars }}
{{- if kindIs "map" $value }}
{{- range $subKey, $subValue := $value }}
{{- if and (kindIs "string" $subValue) (contains "${nvcf.sidecars." $subValue) }}
{{- $name := printf "%s-%s" $key $subKey }}
release-artifact-{{ $name }}{{ if not (hasSuffix "-image" $name) }}-image{{ end }}: {{ $subValue | replace "${nvcf.sidecars.hostname}" $hostname | replace "${nvcf.sidecars.repository}" $repository | quote }}
{{- end }}
{{- end }}
{{- else if and (kindIs "string" $value) (contains "${nvcf.sidecars." $value) }}
release-artifact-{{ $key }}{{ if not (hasSuffix "-image" $key) }}-image{{ end }}: {{ $value | replace "${nvcf.sidecars.hostname}" $hostname | replace "${nvcf.sidecars.repository}" $repository | quote }}
{{- end }}
{{- end }}
{{- end -}}
{{- end -}}

{{/*
Vault Agent Injector Annotations for JWT Auth
*/}}
{{- define "nvcf-api.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: "nvcf-api"
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/auth-config-token-path: "/var/run/secrets/openbao/token"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/home/app/vault"
{{- end }}
