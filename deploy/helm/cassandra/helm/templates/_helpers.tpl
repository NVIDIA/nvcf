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

{{- define "nvcf-cassandra.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}

{{- define "nvcf-cassandra.fullname" -}}
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
Return the proper Docker Image Registry Secret Names for wrapper chart
Supports both industry standard format and backward compatibility:
- Industry standard: [{name: "secret1"}, {name: "secret2"}]
- Bitnami simplified: ["secret1", "secret2"]
- Current format (backward compatibility): [{name: "secret1"}]
*/}}

{{- define "nvcf-cassandra.imagePullSecrets" -}}
  {{- $pullSecrets := list }}

  {{- range .Values.cassandra.global.imagePullSecrets -}}
    {{- if kindIs "map" . -}}
      {{- $pullSecrets = append $pullSecrets .name -}}
    {{- else -}}
      {{- $pullSecrets = append $pullSecrets . -}}
    {{- end }}
  {{- end -}}

  {{- if (not (empty $pullSecrets)) -}}
imagePullSecrets:
    {{- range $pullSecrets | uniq }}
  - name: {{ . }}
    {{- end }}
  {{- end }}
{{- end -}}
