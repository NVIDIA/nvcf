# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

{{/*
Expand the name of the chart.
*/}}
{{- define "nvcf-ui.name" -}}
{{- default .Chart.Name .Values.nvcfUi.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-ui.fullname" -}}
{{- if .Values.nvcfUi.fullnameOverride }}
{{- .Values.nvcfUi.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nvcfUi.nameOverride }}
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
{{- define "nvcf-ui.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvcf-ui.labels" -}}
helm.sh/chart: {{ include "nvcf-ui.chart" . }}
{{ include "nvcf-ui.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-ui.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-ui.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the service account to use. Hardcoded to "nvcf-ui": it must match the
serviceaccount/name bound_claim in the OpenBao nvcf-ui JWT auth role (see the
openbao migrations addon setup_nvcf-ui.sh), or Vault Agent auth fails. Not
overridable on purpose — the two sides share this fixed contract.
*/}}
{{- define "nvcf-ui.serviceAccountName" -}}
nvcf-ui
{{- end }}

{{/*
nvcf-openbao-migrations image reference for the opt-in OpenBao roles hook Job.
Registry is optional; repository and tag are required when the hook is enabled.
*/}}
{{- define "nvcf-ui.openbaoMigrationsImage" -}}
{{- $img := .Values.nvcfUi.openbaoMigrations.image -}}
{{- $registry := $img.registry -}}
{{- $repository := required "openbaoMigrations.image.repository is required" $img.repository -}}
{{- $tag := required "openbaoMigrations.image.tag is required" $img.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{/*
Control-plane fully qualified name.
*/}}
{{- define "nvcf-ui.controlPlane.fullname" -}}
{{- printf "%s-control-plane" (include "nvcf-ui.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Control-plane selector labels. Adds a component label so this Deployment's
selector never matches the server pods (and vice versa).
*/}}
{{- define "nvcf-ui.controlPlane.selectorLabels" -}}
{{ include "nvcf-ui.selectorLabels" . }}
app.kubernetes.io/component: control-plane
{{- end }}

{{/*
Control-plane common labels.
*/}}
{{- define "nvcf-ui.controlPlane.labels" -}}
{{ include "nvcf-ui.labels" . }}
app.kubernetes.io/component: control-plane
{{- end }}

{{/*
Create the name of the control-plane service account to use. It is always
created; an explicit name overrides the generated default.
*/}}
{{- define "nvcf-ui.controlPlane.serviceAccountName" -}}
{{- default (include "nvcf-ui.controlPlane.fullname" .) .Values.nvcfUi.controlPlane.serviceAccount.name }}
{{- end }}

{{/*
This is the OpenBao-audience
Kubernetes token used for Vault Agent auto-auth; it is distinct from the normal
kube API service-account token and from any Vault Agent output token.
*/}}
{{- define "nvcf-ui.openbaoTokenVolumeName" -}}
openbao-token
{{- end -}}

{{- define "nvcf-ui.openbaoTokenMountPath" -}}
/var/run/secrets/openbao
{{- end -}}

{{- define "nvcf-ui.openbaoTokenFileName" -}}
token
{{- end -}}

{{- define "nvcf-ui.openbaoTokenPath" -}}
{{- printf "%s/%s" (include "nvcf-ui.openbaoTokenMountPath" .) (include "nvcf-ui.openbaoTokenFileName" .) -}}
{{- end -}}

{{/*
OpenBao agent-injector annotations.
The agent mints JWTs against nvcf-api, nvct-api, and sis-api via templates
and writes them to /var/run/secrets/vault/tokens.json. The app reads that file;
the agent handles re-minting on expiry.
*/}}
{{- define "nvcf-ui.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ .Values.nvcfUi.vault.role | quote }}
vault.hashicorp.com/auth-path: {{ .Values.nvcfUi.vault.authPath | quote }}
{{- /* Authenticate with the chart-owned, audience-scoped projected token rather
than the default kube SA token, whose aud the jwt role rejects.
The app container mounts this projected token at openbaoTokenMountPath;
agent-copy-volume-mounts (below) copies that mount into the agent init/sidecar,
so the token is available to the agent at the same path. auth-config-token-path is
the file the jwt auth method reads. Do NOT also set
agent-service-account-token-volume-name: it makes the injector add a second mount
of the same volume at the same path, which makes the pod invalid (mountPath must
be unique). */}}
vault.hashicorp.com/auth-config-token-path: {{ include "nvcf-ui.openbaoTokenPath" . | quote }}
vault.hashicorp.com/agent-run-as-same-user: "true"
vault.hashicorp.com/agent-inject-template-file-tokens.json: "/vault/config/templates/tokens.tmpl"
vault.hashicorp.com/secret-volume-path: {{ .Values.nvcfUi.vault.secretVolumePath | quote }}
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
{{- end }}
