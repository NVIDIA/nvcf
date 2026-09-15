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
Chart name and a stable fullname. The "nvsnap.fullname" helper trims
release names so that "nvsnap-agent", "nvsnap-server" etc. remain readable.
*/}}
{{- define "nvsnap.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "nvsnap.fullname" -}}
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

{{- define "nvsnap.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every object the chart creates.
*/}}
{{- define "nvsnap.labels" -}}
helm.sh/chart: {{ include "nvsnap.chart" . }}
app.kubernetes.io/name: {{ include "nvsnap.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/*
Per-component labels and selector labels.

Components: agent, server, blobstore, webhook.

Selector labels include only stable identity (name + instance + component);
they're embedded in Deployment/DaemonSet.spec.selector which is immutable
after first apply. Full labels include version + managed-by, which are
expected to change on upgrades — fine on metadata.labels, fatal on selector.
*/}}
{{- define "nvsnap.agent.selectorLabels" -}}
app: nvsnap-agent
app.kubernetes.io/name: nvsnap-agent
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: agent
{{- end }}

{{- define "nvsnap.agent.labels" -}}
{{ include "nvsnap.labels" . }}
app: nvsnap-agent
app.kubernetes.io/component: agent
{{- end }}

{{- define "nvsnap.server.selectorLabels" -}}
app: nvsnap-server
app.kubernetes.io/name: nvsnap-server
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end }}

{{- define "nvsnap.server.labels" -}}
{{ include "nvsnap.labels" . }}
app: nvsnap-server
app.kubernetes.io/component: server
{{- end }}

{{- define "nvsnap.blobstore.selectorLabels" -}}
app: nvsnap-blobstore
app.kubernetes.io/name: nvsnap-blobstore
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: blobstore
{{- end }}

{{- define "nvsnap.blobstore.labels" -}}
{{ include "nvsnap.labels" . }}
app: nvsnap-blobstore
app.kubernetes.io/component: blobstore
{{- end }}

{{/*
Image references. Each helper resolves <registry>/<repository>:<tag>
where registry defaults to .Values.nvsnap.imageRegistry but can be
overridden per-component. Tag defaults to the chart's appVersion.

Why .nvsnap.* and not .global.* — Helm auto-merges `global:` into every
subchart's `.Values.global`, so a top-level `global.imageRegistry` of
`nvcr.io/...` leaks into Jaeger / cert-manager / gpu-operator and
causes them to try to pull their upstream images from our private
registry. Keeping our registry under our own namespace prevents that.
*/}}
{{- define "nvsnap.image" -}}
{{- $registry := .registry | default .ctx.Values.nvsnap.imageRegistry -}}
{{- /* Image tags are mandatory. We used to fall back to .Chart.AppVersion
       when .tag was empty, but that silently drifts every image that
       isn't explicitly --set (notably blobstore) to whatever Chart.yaml
       says, even if no such tag was ever built+pushed. Fail loudly
       instead so the install/upgrade can't render a known-broken
       reference. See nvsnap MR following the v0.0.5 blobstore
       ImagePullBackOff on GCP-H100-a (2026-06-02). */ -}}
{{- if not .tag -}}
{{- fail (printf "nvsnap.image: tag is required for repository %q (set %s.image.tag in values.yaml or --set)" .repository .repository) -}}
{{- end -}}
{{- printf "%s/%s:%s" $registry .repository .tag -}}
{{- end }}

{{- define "nvsnap.agent.image" -}}
{{- include "nvsnap.image" (dict "ctx" . "registry" .Values.agent.image.registry "repository" .Values.agent.image.repository "tag" .Values.agent.image.tag) -}}
{{- end }}

{{- define "nvsnap.server.image" -}}
{{- include "nvsnap.image" (dict "ctx" . "registry" .Values.server.image.registry "repository" .Values.server.image.repository "tag" .Values.server.image.tag) -}}
{{- end }}

{{- define "nvsnap.blobstore.image" -}}
{{- include "nvsnap.image" (dict "ctx" . "registry" .Values.blobstore.image.registry "repository" .Values.blobstore.image.repository "tag" .Values.blobstore.image.tag) -}}
{{- end }}

{{- /* nvsnap.l2wait.image — image ref for the nvsnap-l2-wait init
       container (nvsnap#147). Uses the agent.l2.waitImage block;
       falls through to nvsnap.imageRegistry like every other NvSnap
       image. */ -}}
{{- define "nvsnap.l2wait.image" -}}
{{- include "nvsnap.image" (dict "ctx" . "registry" .Values.agent.l2.waitImage.registry "repository" .Values.agent.l2.waitImage.repository "tag" .Values.agent.l2.waitImage.tag) -}}
{{- end }}

{{- /* nvsnap.restoreBundle.image helper was retired in nvsnap#184.
       The webhook no longer injects a per-pod init container that
       pulls a nvsnap image; instead the nvsnap-agent DaemonSet itself
       stages the restore bundle to /var/lib/nvsnap/bundle via its
       nvsnap-bundle-stage initContainer, and the webhook injects
       hostPath mounts from there onto function pods. No
       cross-registry mirror required. */ -}}

{{- define "nvsnap.builder.image" -}}
{{- /* args: ctx, name (e.g. "uvloop-builder"). Same mandatory-tag rule
       as nvsnap.image — if nvsnap.builderTag is empty the user has to
       set it explicitly. */ -}}
{{- $reg := .ctx.Values.nvsnap.imageRegistry -}}
{{- if not .ctx.Values.nvsnap.builderTag -}}
{{- fail (printf "nvsnap.builder.image: nvsnap.builderTag is required (rendering %q)" .name) -}}
{{- end -}}
{{- printf "%s/%s:%s" $reg .name .ctx.Values.nvsnap.builderTag -}}
{{- end }}

{{/*
Image-pull-secrets block, rendered if any are configured. Used at the
PodSpec level (not Deployment-level — Kubernetes requires it on pods).
*/}}
{{- define "nvsnap.imagePullSecrets" -}}
{{- with .Values.nvsnap.imagePullSecrets }}
imagePullSecrets:
{{- range . }}
  - name: {{ . }}
{{- end }}
{{- end }}
{{- end }}
