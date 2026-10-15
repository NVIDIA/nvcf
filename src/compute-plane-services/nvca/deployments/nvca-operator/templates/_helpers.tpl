{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/}}
{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "nvcaop.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcaop.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nvcaop.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "nvcaop.labels" -}}
helm.sh/chart: {{ include "nvcaop.chart" . }}
app.kubernetes.io/name: {{ include "nvcaop.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Base selector labels, to use when defining component selector labels.
*/}}
{{- define "nvcaop.baseSelectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcaop.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ClusterName	truncated at 32 chars
*/}}
{{- define "nvcaop.clustername" -}}
{{- .Values.clustername | trunc 32 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "nvcaop.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "nvcaop.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Validate imagePullSecretName is specified when generateImagePullSecret is false
*/}}
{{- define "nvcaop.validateImagePullSecret" -}}
{{- if not .Values.generateImagePullSecret -}}
{{- if not .Values.imagePullSecretName -}}
{{- fail "imagePullSecretName must be specified when generateImagePullSecret is false" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
ImagePullSecret for images.
*/}}
{{- define "nvcaop.generatedImagePullSecret" }}
{{- $username := .Values.ngcConfig.username }}
{{- $serviceKey := .Values.ngcConfig.serviceKey | required "NGC service key is required to create a pull secret" }}
{{- $auths := dict }}
{{- $auth := dict "username" $username "password" $serviceKey "auth" (printf "%s:%s" $username $serviceKey | b64enc) }}
{{- range $i, $repo := (list .Values.image.repository .Values.nvcaImage.repositoryOverride) }}
{{- if $repo }}
{{- $_ := set $auths (splitList "/" $repo | first) $auth }}
{{- end }}
{{- end }}
{{- printf "{\"auths\":%s}" ($auths | toJson) | b64enc }}
{{- end }}

{{/*
Convert list to JSON if non-empty otherwise return []
Usage: {{ include "nvcaop.jsonListOrEmpty" <list> }}
*/}}
{{- define "nvcaop.jsonListOrEmpty" -}}
{{- $l := (default (list) .) | compact -}}
{{- if gt (len $l) 0 -}}
{{ $l | toJson }}
{{- else -}}
[]
{{- end -}}
{{- end -}}

{{/*
Get the imageCredHelper repository based on image.repository
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.imageCredHelperRepository" (dict "imageRepository" .Values.helmManaged.imageCredHelper.imageRepository "defaultRepository" .Values.image.repository) }}
*/}}
{{- define "nvcaop.imageCredHelperRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper
{{- end -}}
{{- end -}}

{{/*
Get the OTel collector repository based on image.repository
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.otelCollectorRepository" (dict "imageRepository" .Values.otelCollector.imageRepository "defaultRepository" .Values.image.repository) }}
*/}}
{{- define "nvcaop.otelCollectorRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector
{{- end -}}
{{- end -}}

{{/*
Check if cluster validator is enabled (nil-safe).
Returns non-empty string if enabled, empty string if disabled.
Usage: {{- if (include "nvcaop.clusterValidatorEnabled" .) -}}
*/}}
{{- define "nvcaop.clusterValidatorEnabled" -}}
{{- $cv := .Values.clusterValidator | default dict -}}
{{- if ($cv.enabled | default false) -}}true{{- end -}}
{{- end -}}

{{/*
Cluster validator config with chart defaults merged in.
Returns YAML; callers `| fromYaml` it and dereference safely.

Defends against `helm upgrade --reuse-values` from a release created before
clusterValidator existed: in that case the stored values have no
clusterValidator.image / .resources / .schedule sections, and Helm does not
fall back to the new chart's values.yaml defaults for absent keys. Without
this helper, deployment.yaml / cronjob.yaml hit a nil pointer when
dereferencing .Values.clusterValidator.image.repository.

`merge` keeps existing user values when present and only fills in defaults
for absent keys, so explicit overrides are preserved.

Usage: {{- $cv := include "nvcaop.clusterValidatorConfig" . | fromYaml -}}
*/}}
{{- define "nvcaop.clusterValidatorConfig" -}}
{{- /* Defaults must mirror values.yaml so a --reuse-values upgrade gets
       exactly the same effective config as a fresh install. */ -}}
{{- $defaults := dict
    "image" (dict "repository" "" "tag" "" "pullPolicy" "IfNotPresent")
    "schedule" "0 */3 * * *"
    "configMapName" "cluster-validator-network-checks"
    "networkChecks" (dict)
    "resources" (dict
      "requests" (dict "cpu" "100m" "memory" "64Mi")
      "limits"   (dict "cpu" "200m" "memory" "128Mi"))
-}}
{{- /* `merge (dict) user defaults` writes into a fresh empty dict so
       .Values.clusterValidator is never mutated in-place. Sprig's
       `merge dst src...` modifies dst; if dst were $cv directly, the
       default keys would be written back into .Values.clusterValidator
       and any other template that reads .Values directly after the
       first render would see merged-in defaults instead of original
       user values. */ -}}
{{- $user := .Values.clusterValidator | default dict -}}
{{- merge (dict) $user $defaults | toYaml -}}
{{- end -}}

{{/*
Get the cluster-validator repository based on image.repository
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.clusterValidatorRepository" (dict "imageRepository" .Values.clusterValidator.image.repository "defaultRepository" .Values.image.repository) }}
*/}}
{{- define "nvcaop.clusterValidatorRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/cluster-validator
{{- end -}}
{{- end -}}