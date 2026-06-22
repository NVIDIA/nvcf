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
{{- define "helm-nvcf-invocation-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "helm-nvcf-invocation-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "helm-nvcf-invocation-service.labels" -}}
helm.sh/chart: {{ include "helm-nvcf-invocation-service.chart" . }}
{{ include "helm-nvcf-invocation-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "helm-nvcf-invocation-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "helm-nvcf-invocation-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Build the merged Values object passed to the OTEL collector sidecar /
volumes / configmap named templates. Merges the consumer's overrides
under .Values.otelCollector with derived fields the sidecar
reads (env, source.metrics, source.logging).
*/}}
{{- define "otel-collector.values" -}}
{{- $derivedDict := dict }}
{{- $derivedDict := merge $derivedDict (include "otel-collector.env" . | fromYaml) }}
{{- with .Values.settings.telemetry }}
    {{- $metricsDict := dict "port" .port "endpoint" .metrics.endpoint "scrapeInterval" .metrics.scrapeInterval }}
    {{- $loggingDict := dict "volume" .logging.volume "path" .logging.path "enabled" (ne .logging.output "terminal") }}
    {{- $derivedDict := set $derivedDict "source" (dict "metrics" $metricsDict "logging" $loggingDict) }}
{{- end }}
{{- $result := merge $derivedDict .Values.otelCollector }}
{{- toYaml $result }}
{{- end }}

{{- define "otel-collector.env" }}
env:
  OTEL_SERVICE_NAME: {{ include "helm-nvcf-invocation-service.name" . }}
  {{- if .Chart.AppVersion }}
  OTEL_SERVICE_VERSION: {{ .Chart.AppVersion }}
  {{- end }}
  OTEL_APP_IMAGE_NAME: {{ .Values.image.repository }}
  OTEL_APP_IMAGE_TAG: {{ .Values.image.tag }}
{{- end }}

{{/*
OTEL collector sidecar container spec. Receives a merged Values dict
that combines the consumer's .otelCollector overrides with the defaults
from the previous upstream otelCollector sub-chart.
*/}}
{{- define "otelCollector.sidecar" }}
- name: {{ .Values.image.name }}
  image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  command: {{ .Values.entrypoint.command }}
  args: {{ concat (.Values.entrypoint.args | default list) (list (print "--config=" .Values.entrypoint.configPath)) }}
  livenessProbe:
    httpGet:
      path: {{ .Values.extensions.health_check.path }}
      port: {{ index (split ":" .Values.extensions.health_check.endpoint) "_1" }}
    initialDelaySeconds: 30  # Delay before liveness probe is initiated
    periodSeconds: 10  # How often to perform the probe
  volumeMounts:
    - name: otel-collector-config
      mountPath: {{ .Values.entrypoint.configPath }}
      subPath: {{ base .Values.entrypoint.configPath }}
      readOnly: true
    {{- if .Values.source.logging.enabled }}
    - name: {{ .Values.source.logging.volume }}
      mountPath: {{ .Values.source.logging.path }}
    {{- end }}
  resources:
    {{- toYaml .Values.resources | nindent 4 }}
  env:
    {{- range $key, $value := .Values.env }}
    - name: {{ $key }}
      value: "{{ $value }}"
    {{- end }}
    - name: K8S_POD_NAME
      valueFrom:
        fieldRef:
          fieldPath: metadata.name
    - name: K8S_POD_NAMESPACE
      valueFrom:
        fieldRef:
          fieldPath: metadata.namespace
    - name: K8S_POD_UID
      valueFrom:
        fieldRef:
          fieldPath: metadata.uid
    - name: K8S_POD_IP
      valueFrom:
        fieldRef:
          fieldPath: status.podIP
{{- end }}

{{- define "otelCollector.volumes" }}
- name: otel-collector-config
  configMap:
    name: otel-collector-configmap
{{- end }}

{{- define "otelCollector.configmap" }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector-configmap
data:
  {{ base .Values.entrypoint.configPath }}: |-
    receivers:
      otlp:
        protocols:
          grpc:
      jaeger:
        protocols:
          grpc:
          thrift_http:
          thrift_compact:
          thrift_binary:
      prometheus:
        config:
          scrape_configs:
          - job_name: '${OTEL_SERVICE_NAME}'
            metrics_path: {{ .Values.source.metrics.endpoint }}
            scrape_interval: {{ .Values.source.metrics.scrapeInterval }}
            static_configs:
            - targets: ['127.0.0.1:{{ .Values.source.metrics.port }}']
      {{- if .Values.source.logging.enabled }}
      filelog:
        include: ["{{ .Values.source.logging.path }}/*.json"]
        start_at: beginning
        operators:
        - type: json_parser
      {{- end }}

    exporters:
      logging:
        loglevel: debug
      {{- if .Values.exporters }}
      {{- toYaml .Values.exporters | nindent 6 }}
      {{- end }}

    extensions:
      {{- if .Values.extensions }}
      {{- toYaml .Values.extensions | nindent 6 }}
      {{- end }}

    processors:
      batch:
      resource:
        attributes:
          # key: service.name
          # value: set by prometheus receiver according to job_name
          - key: service.namespace
            value: ${K8S_POD_NAMESPACE}
            action: upsert
          - key: service.instance.id
            value: ${K8S_POD_NAME}:{{ .Values.source.metrics.port }}
            action: upsert
          - key: service.version
            value: ${OTEL_SERVICE_VERSION}
            action: upsert
          - key: container.image.name
            value: ${OTEL_APP_IMAGE_NAME}
            action: upsert
          - key: container.image.tags
            value: ["${OTEL_APP_IMAGE_TAG}"]
            action: upsert
          - key: server.address
            value: ${K8S_POD_IP}
            action: upsert
          - key: k8s.pod.uid
            value: ${K8S_POD_UID}
            action: upsert

    service:
      {{- if .Values.extensions }}
      extensions:
      {{- range $ext, $_ := .Values.extensions }}
      - {{ $ext }}
      {{- end }}
      {{- end }}
      pipelines:
        traces:
          receivers: [jaeger,otlp]
          processors: [batch]
          exporters: [{{ .Values.service.pipelines.traces.exporters | join ", " }}]
        metrics:
          receivers: [prometheus]
          processors: [batch,resource]
          exporters: [{{ .Values.service.pipelines.metrics.exporters | join ", " }}]
      {{- if .Values.source.logging.enabled }}
        logs:
          receivers: [filelog]
          processors: [batch]
          exporters: [{{ .Values.service.pipelines.logs.exporters | join ", " }}]
      {{- end }}
{{- end }}
