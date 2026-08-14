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

Defaults to the literal "event-ledger" rather than .Chart.Name so the
runtime service identity (resource names, selectors, container name) stays
stable and matches the in-cluster DNS contract (event-ledger.nvcf.svc),
independent of the OCI chart package name. Override via
.Values.eventLedger.nameOverride.
*/}}
{{- define "nvcf-event-ledger.name" -}}
{{- default "event-ledger" .Values.eventLedger.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-event-ledger.fullname" -}}
{{- if .Values.eventLedger.fullnameOverride }}
{{- .Values.eventLedger.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default "event-ledger" .Values.eventLedger.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Allow the release namespace to be overridden via .Values.eventLedger.namespace,
falling back to .Release.Namespace.
*/}}
{{- define "nvcf-event-ledger.namespace" -}}
{{- default .Release.Namespace .Values.eventLedger.namespace -}}
{{- end -}}

{{/*
Derive the full image reference. Fails the render if either image.registry
or image.repository is missing so the chart cannot install a default/invalid
image.
*/}}
{{- define "nvcf-event-ledger.image" -}}
{{- $registry := required "A valid image registry (.Values.eventLedger.image.registry) is required!" .Values.eventLedger.image.registry -}}
{{- $repository := required "A valid image repository (.Values.eventLedger.image.repository) is required!" .Values.eventLedger.image.repository -}}
{{- $name := .Values.eventLedger.image.name | default "event-ledger" -}}
{{- $tag := .Values.eventLedger.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s/%s:%s" $registry $repository $name $tag -}}
{{- end -}}

{{/*
OpenBao projected service-account token helpers. These four helpers define
the chart-owned contract for the Vault Agent Kubernetes auto-auth token volume.
Use these in annotations and the workload template instead of repeating literals.
*/}}
{{- define "nvcf-event-ledger.openbaoTokenVolumeName" -}}
openbao-token
{{- end -}}

{{- define "nvcf-event-ledger.openbaoTokenMountPath" -}}
/var/run/secrets/openbao
{{- end -}}

{{- define "nvcf-event-ledger.openbaoTokenFileName" -}}
token
{{- end -}}

{{- define "nvcf-event-ledger.openbaoTokenPath" -}}
{{- printf "%s/%s" (include "nvcf-event-ledger.openbaoTokenMountPath" .) (include "nvcf-event-ledger.openbaoTokenFileName" .) -}}
{{- end -}}

{{/*
Hashicorp Vault Agent Injector annotations. Always rendered onto the pod
so the injector mints /vault/secrets/secrets.json at pod start from the
OpenBao secret paths. The template content is rendered into a ConfigMap by
templates/configmap-vault-agent-template.yaml and mounted into the pod at
/vault/config/templates.
*/}}
{{- define "nvcf-event-ledger.vaultAnnotations" -}}
{{- $role := required "A valid Vault auth role (.Values.eventLedger.vault.role) is required!" .Values.eventLedger.vault.role -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ $role | quote }}
vault.hashicorp.com/auth-path: {{ .Values.eventLedger.vault.jwtAuthPath | default "auth/jwt" | quote }}
vault.hashicorp.com/agent-service-account-token-volume-name: {{ include "nvcf-event-ledger.openbaoTokenVolumeName" . | quote }}
vault.hashicorp.com/auth-config-token-path: {{ include "nvcf-event-ledger.openbaoTokenPath" . | quote }}
vault.hashicorp.com/agent-copy-volume-mounts: {{ include "nvcf-event-ledger.fullname" . | quote }}
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nvcf-event-ledger.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvcf-event-ledger.labels" -}}
helm.sh/chart: {{ include "nvcf-event-ledger.chart" . }}
{{ include "nvcf-event-ledger.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-event-ledger.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-event-ledger.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "nvcf-event-ledger.serviceAccountName" -}}
{{- if .Values.eventLedger.serviceAccount.create }}
{{- default (include "nvcf-event-ledger.fullname" .) .Values.eventLedger.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.eventLedger.serviceAccount.name }}
{{- end }}
{{- end }}
