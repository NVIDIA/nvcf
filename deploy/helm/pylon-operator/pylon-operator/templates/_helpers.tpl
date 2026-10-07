{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "pylon-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name, truncated to the 63 character DNS
label limit. A release name that contains the chart name is used as is.
*/}}
{{- define "pylon-operator.fullname" -}}
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
Chart name and version as used by the chart label.
*/}}
{{- define "pylon-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "pylon-operator.labels" -}}
helm.sh/chart: {{ include "pylon-operator.chart" . }}
{{ include "pylon-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "pylon-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pylon-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the operator ServiceAccount.
*/}}
{{- define "pylon-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "pylon-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Name of the Secret that holds the cluster credential. This is exactly the
value of --cluster-credential-secret.
*/}}
{{- define "pylon-operator.credentialSecretName" -}}
{{- if .Values.credential.existingSecret -}}
{{- .Values.credential.existingSecret -}}
{{- else -}}
{{- printf "%s-cluster-credential" (include "pylon-operator.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
True when the chart owns the credential Secret.
*/}}
{{- define "pylon-operator.generateCredential" -}}
{{- if and (not .Values.credential.existingSecret) .Values.credential.generate -}}
true
{{- end -}}
{{- end -}}

{{/*
Join a repository and a tag or a sha256 digest into an image reference.
Usage: include "pylon-operator.imageRef" (list $repository $tag)
*/}}
{{- define "pylon-operator.imageRef" -}}
{{- $repository := index . 0 -}}
{{- $tag := index . 1 | toString -}}
{{- if hasPrefix "sha256:" $tag -}}
{{- printf "%s@%s" $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Operator image. The repository is required and the tag defaults to the chart
appVersion.
*/}}
{{- define "pylon-operator.image" -}}
{{- $repository := required "image.repository is required" .Values.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- include "pylon-operator.imageRef" (list $repository $tag) -}}
{{- end -}}

{{/*
Pylon transport image, passed as --pylon-image.
*/}}
{{- define "pylon-operator.pylonImage" -}}
{{- $repository := required "pylon.image.repository is required" .Values.pylon.image.repository -}}
{{- $tag := required "pylon.image.tag is required" .Values.pylon.image.tag -}}
{{- include "pylon-operator.imageRef" (list $repository $tag) -}}
{{- end -}}

{{/*
Names of the transport pods' image pull Secrets, comma-separated, passed as
--pylon-image-pull-secrets. Empty when none are set.
*/}}
{{- define "pylon-operator.pylonImagePullSecrets" -}}
{{- $names := list -}}
{{- range .Values.pylon.imagePullSecrets -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}

{{/*
Validate the values the operator needs to start. Every missing required value
is reported at once. Renders nothing.
*/}}
{{- define "pylon-operator.validate" -}}
{{- $missing := list -}}
{{- if not .Values.image.repository -}}
{{- $missing = append $missing "image.repository" -}}
{{- end -}}
{{- if not .Values.clusterId -}}
{{- $missing = append $missing "clusterId" -}}
{{- end -}}
{{- if not .Values.router.grpcAddress -}}
{{- $missing = append $missing "router.grpcAddress" -}}
{{- end -}}
{{- if not .Values.pylon.image.repository -}}
{{- $missing = append $missing "pylon.image.repository" -}}
{{- end -}}
{{- if not .Values.pylon.image.tag -}}
{{- $missing = append $missing "pylon.image.tag" -}}
{{- end -}}
{{- if $missing -}}
{{- fail (printf "pylon-operator: set the required values: %s" (join ", " $missing)) -}}
{{- end -}}
{{- $dnsLabel := "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" -}}
{{- if or (gt (len .Values.clusterId) 63) (not (regexMatch $dnsLabel .Values.clusterId)) -}}
{{- fail (printf "pylon-operator: clusterId %q must be a DNS label: lowercase letters, digits and '-', at most 63 characters" .Values.clusterId) -}}
{{- end -}}
{{- range .Values.watchNamespaces -}}
{{- if or (gt (len .) 63) (not (regexMatch $dnsLabel .)) -}}
{{- fail (printf "pylon-operator: watchNamespaces entry %q is not a namespace name" .) -}}
{{- end -}}
{{- end -}}
{{- if lt (int .Values.transport.replicas) 1 -}}
{{- fail "pylon-operator: transport.replicas must be at least 1" -}}
{{- end -}}
{{- if le (float64 .Values.transport.initialInputTPS) 0.0 -}}
{{- fail "pylon-operator: transport.initialInputTPS must be a positive number" -}}
{{- end -}}
{{- if ne .Values.credential.key "cluster-token" -}}
{{- fail (printf "pylon-operator: credential.key must be cluster-token, got %q; the operator reads only that key" .Values.credential.key) -}}
{{- end -}}
{{- end -}}
