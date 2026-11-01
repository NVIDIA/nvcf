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

{{/*
Expand the name of the chart.
*/}}
{{- define "multi-node-test.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "multi-node-test.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
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
{{- define "multi-node-test.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "multi-node-test.labels" -}}
helm.sh/chart: {{ include "multi-node-test.chart" . }}
{{ include "multi-node-test.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "multi-node-test.selectorLabels" -}}
app.kubernetes.io/name: {{ include "multi-node-test.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "multi-node-test.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "multi-node-test.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "imagePullSecret" }}
{{- with .Values.imagePullSecret }}
{{- printf "{\"auths\":{\"%s\":{\"username\":\"%s\",\"password\":\"%s\",\"email\":\"%s\",\"auth\":\"%s\"}}}" .registry .username .password .email (printf "%s:%s" .username .password | b64enc) | b64enc }}
{{- end }}
{{- end }}

{{/*
Detect the cluster profile from live Kubernetes objects available to Helm.
*/}}
{{- define "multi-node-test.detectClusterProfile" -}}
{{- $detection := .Values.clusterProfileDetection | default dict -}}
{{- $roceDeviceClassName := default "roce.networking.k8s.aws" $detection.roceDeviceClassName -}}
{{- $efaResourceName := default "vpc.amazonaws.com/efa" $detection.efaResourceName -}}
{{- $mlnxResourceName := default "nvidia.com/mlnxnics" $detection.mlnxResourceName -}}
{{- $deviceClass := dict -}}
{{- if .Capabilities.APIVersions.Has "resource.k8s.io/v1/DeviceClass" -}}
{{- $deviceClass = lookup "resource.k8s.io/v1" "DeviceClass" "" $roceDeviceClassName -}}
{{- end -}}
{{- if not (empty $deviceClass) -}}
aws-gb300
{{- else -}}
{{- $nodes := lookup "v1" "Node" "" "" | default dict -}}
{{- $hasEfa := false -}}
{{- $hasMlnx := false -}}
{{- range $node := (get $nodes "items" | default list) -}}
{{- $allocatable := dig "status" "allocatable" dict $node -}}
{{- if hasKey $allocatable $efaResourceName -}}
{{- $hasEfa = true -}}
{{- end -}}
{{- if hasKey $allocatable $mlnxResourceName -}}
{{- $hasMlnx = true -}}
{{- end -}}
{{- end -}}
{{- if $hasEfa -}}
aws-gb200
{{- else if $hasMlnx -}}
ncp-gb200
{{- else -}}
{{- fail (printf "clusterProfile=auto could not identify a supported cluster profile. Set clusterProfile to one of aws-gb200, aws-gb300, or ncp-gb200, or grant Helm permission to read DeviceClass/%s and list Nodes with allocatable %s or %s" $roceDeviceClassName $efaResourceName $mlnxResourceName) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Resolve the selected cluster profile name.
*/}}
{{- define "multi-node-test.clusterProfileName" -}}
{{- $requested := default "ncp-gb200" .Values.clusterProfile -}}
{{- $profiles := .Values.clusterProfiles | default dict -}}
{{- $profileName := $requested -}}
{{- if eq $requested "auto" -}}
{{- if not (.Values.clusterProfileDetection.enabled | default false) -}}
{{- fail "clusterProfile=auto requires clusterProfileDetection.enabled=true" -}}
{{- end -}}
{{- $profileName = include "multi-node-test.detectClusterProfile" . -}}
{{- end -}}
{{- if not (hasKey $profiles $profileName) -}}
{{- fail (printf "unsupported clusterProfile %q. Set clusterProfile to one of aws-gb200, aws-gb300, ncp-gb200, or auto" $profileName) -}}
{{- end -}}
{{- $profileName -}}
{{- end }}

{{/*
Build effective profile values. Top-level chart values override selected
profile values where old override files used them.
*/}}
{{- define "multi-node-test.effectiveProfileValues" -}}
{{- $profileName := include "multi-node-test.clusterProfileName" . -}}
{{- $profile := deepCopy (index .Values.clusterProfiles $profileName) -}}
{{- with .Values.nodesPerInstance -}}
{{- $_ := set $profile "nodesPerInstance" . -}}
{{- end -}}
{{- with .Values.image -}}
{{- $_ := set $profile "image" (mergeOverwrite (deepCopy (get $profile "image" | default dict)) .) -}}
{{- end -}}
{{- with .Values.resources -}}
{{- $_ := set $profile "resources" . -}}
{{- end -}}
{{- with .Values.podAnnotations -}}
{{- $_ := set $profile "podAnnotations" (mergeOverwrite (deepCopy (get $profile "podAnnotations" | default dict)) .) -}}
{{- end -}}
{{- with .Values.securityContext -}}
{{- $_ := set $profile "securityContext" . -}}
{{- end -}}
{{- with .Values.resourceClaimTemplate -}}
{{- $_ := set $profile "resourceClaimTemplate" (mergeOverwrite (deepCopy (get $profile "resourceClaimTemplate" | default dict)) .) -}}
{{- end -}}
{{- toYaml $profile -}}
{{- end }}
