# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

{{- define "compute-plane-webhook-pki.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "compute-plane-webhook-pki.labels" -}}
helm.sh/chart: {{ include "compute-plane-webhook-pki.name" . }}
app.kubernetes.io/name: {{ include "compute-plane-webhook-pki.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
