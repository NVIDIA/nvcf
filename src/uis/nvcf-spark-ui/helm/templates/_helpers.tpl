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
{{- define "nvcf-spark-ui.name" -}}
{{- default .Chart.Name .Values.nvcfSparkUi.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcf-spark-ui.fullname" -}}
{{- if .Values.nvcfSparkUi.fullnameOverride }}
{{- .Values.nvcfSparkUi.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nvcfSparkUi.nameOverride }}
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
{{- define "nvcf-spark-ui.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvcf-spark-ui.labels" -}}
helm.sh/chart: {{ include "nvcf-spark-ui.chart" . }}
{{ include "nvcf-spark-ui.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvcf-spark-ui.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcf-spark-ui.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the ServiceAccount.
*/}}
{{- define "nvcf-spark-ui.serviceAccountName" -}}
{{- include "nvcf-spark-ui.fullname" . }}
{{- end }}

{{/*
Where the gateway key and CA are mounted in the container.
*/}}
{{- define "nvcf-spark-ui.apiKeyDir" -}}
/var/run/secrets/demo-ui
{{- end }}

{{- define "nvcf-spark-ui.caDir" -}}
/etc/llm-gateway-ca
{{- end }}

{{- define "nvcf-spark-ui.recipesDir" -}}
/etc/demo-ui/recipes
{{- end }}

{{/*
The recipe catalog ConfigMap: nvcfSparkUi.recipes.configMap, or <fullname>-recipes.
*/}}
{{- define "nvcf-spark-ui.recipesConfigMap" -}}
{{- .Values.nvcfSparkUi.recipes.configMap | default (printf "%s-recipes" (include "nvcf-spark-ui.fullname" .)) -}}
{{- end }}

{{/*
Checksum of the gateway key, to roll the pod when the key rotates. Kubernetes
doesn't restart pods when a mounted Secret changes, and the BFF reads the key
once at startup. The stack chart owns the Secret, so it is read with lookup;
during helm template or a dry run lookup finds nothing and the annotation is
left out.
*/}}
{{- define "nvcf-spark-ui.apiKeyChecksum" -}}
{{- $apiKey := .Values.nvcfSparkUi.gateway.apiKey -}}
{{- $secret := lookup "v1" "Secret" .Release.Namespace $apiKey.secret -}}
{{- if and $secret $secret.data (hasKey $secret.data $apiKey.key) -}}
{{- index $secret.data $apiKey.key | sha256sum -}}
{{- end -}}
{{- end }}
