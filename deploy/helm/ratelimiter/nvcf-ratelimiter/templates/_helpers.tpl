{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/}}

{{/*
Expand the name of the chart.

Defaults to the literal "nvcf-ratelimiter" rather than .Chart.Name so the
runtime service identity (resource names, selectors, container name) stays
stable even though the OCI chart package is published as
"helm-nvcf-rate-limiter". Override via .Values.rateLimiter.nameOverride.
*/}}
{{- define "nvcf-ratelimiter.name" -}}
{{- default "nvcf-ratelimiter" .Values.rateLimiter.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-ratelimiter.fullname" -}}
{{- if .Values.rateLimiter.fullnameOverride }}
{{- .Values.rateLimiter.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default "nvcf-ratelimiter" .Values.rateLimiter.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Allow the release namespace to be overridden via .Values.rateLimiter.namespace,
falling back to .Release.Namespace.
*/}}
{{- define "nvcf-ratelimiter.namespace" -}}
{{- default .Release.Namespace .Values.rateLimiter.namespace -}}
{{- end -}}

{{/*
Derive the full image reference. Fails the render if either image.registry
or image.repository is missing so the chart cannot install a default/invalid
image.
*/}}
{{- define "nvcf-ratelimiter.image" -}}
{{- $registry := required "A valid image registry (.Values.rateLimiter.image.registry) is required!" .Values.rateLimiter.image.registry -}}
{{- $repository := required "A valid image repository (.Values.rateLimiter.image.repository) is required!" .Values.rateLimiter.image.repository -}}
{{- $name := .Values.rateLimiter.image.name | default "nvcf-ratelimiter" -}}
{{- $tag := .Values.rateLimiter.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s/%s:%s" $registry $repository $name $tag -}}
{{- end -}}

{{/*
OAuth2 issuer + audience for inbound JWT validation, derived from the
service fullname + namespace to match the Service DNS and the JWT auth role
provisioned by nvcf-openbao-migrations.
*/}}
{{- define "nvcf-ratelimiter.oauth2Issuer" -}}
{{- printf "http://%s.%s.svc.cluster.local" (include "nvcf-ratelimiter.fullname" .) (include "nvcf-ratelimiter.namespace" .) -}}
{{- end -}}
{{- define "nvcf-ratelimiter.audience" -}}
{{- printf "s:%s" (include "nvcf-ratelimiter.fullname" .) -}}
{{- end -}}

{{/*
True when the ratelimiter runs as a multi-pod Olric cluster — either a static
replicaCount > 1, or HPA enabled with maxReplicas > 1. Gates the Olric
peer-discovery RBAC so it is present on any path that can exceed one pod.
*/}}
{{- define "nvcf-ratelimiter.olricClustered" -}}
{{- if or (gt (int .Values.rateLimiter.replicaCount) 1) (and .Values.rateLimiter.autoscaling.enabled (gt (int .Values.rateLimiter.autoscaling.maxReplicas) 1)) -}}
true
{{- end -}}
{{- end -}}

{{/*
Hashicorp Vault Agent Injector annotations. Always rendered onto the pod
so the injector mints /vault/secrets/secrets.json at pod start from the
OpenBao signer path. The template content is rendered into a ConfigMap by
templates/configmap-vault-agent-template.yaml and mounted into the pod at
/vault/config/templates.
*/}}
{{- define "nvcf-ratelimiter.vaultAnnotations" -}}
{{- $role := required "A valid Vault auth role (.Values.rateLimiter.vault.role) is required!" .Values.rateLimiter.vault.role -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ $role | quote }}
vault.hashicorp.com/auth-path: {{ .Values.rateLimiter.vault.jwtAuthPath | default "auth/jwt" | quote }}
{{- /* Point the agent's kubernetes auto-auth at the OpenBao-audience token,
       which is mounted off the standard SA path so the default automounted
       token can serve Olric's in-cluster peer discovery.
       NOTE: this literal must stay in sync with rateLimiter.volumeMounts for
       the `openbao-token` volume in values.yaml (mountPath + projected path).
       If you change that mount, change this annotation too. */}}
vault.hashicorp.com/auth-config-token-path: "/var/run/secrets/openbao/token"
vault.hashicorp.com/agent-copy-volume-mounts: {{ include "nvcf-ratelimiter.fullname" . | quote }}
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nvcf-ratelimiter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvcf-ratelimiter.labels" -}}
helm.sh/chart: {{ include "nvcf-ratelimiter.chart" . }}
{{ include "nvcf-ratelimiter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-ratelimiter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-ratelimiter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "nvcf-ratelimiter.serviceAccountName" -}}
{{- if .Values.rateLimiter.serviceAccount.create }}
{{- default (include "nvcf-ratelimiter.fullname" .) .Values.rateLimiter.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.rateLimiter.serviceAccount.name }}
{{- end }}
{{- end }} 