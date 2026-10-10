{{/* SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0 */}}

{{- define "matrix.name" -}}{{ .Values.helmChartServiceName }}{{- end -}}

{{/* The engine's command line, for this pod's rank ($R in the shell). */}}
{{- define "matrix.serve" -}}
{{- $v := .Values -}}
{{- if eq $v.engine "sglang" -}}
python3 -m sglang.launch_server --model-path {{ $v.model }} --host 0.0.0.0 --port {{ $v.port }} --context-length {{ $v.maxModelLen }} --mem-fraction-static {{ $v.gpuMemoryUtilization }} --tp {{ mul $v.podsPerInstance $v.gpusPerPod }}{{ if gt (int $v.podsPerInstance) 1 }} --nnodes {{ $v.podsPerInstance }} --node-rank $R --dist-init-addr $MASTER:5000{{ end }}
{{- else -}}
vllm serve {{ $v.model }} --host 0.0.0.0 --port {{ $v.port }} --max-model-len {{ $v.maxModelLen }} --gpu-memory-utilization {{ $v.gpuMemoryUtilization }} --tensor-parallel-size {{ mul $v.podsPerInstance $v.gpusPerPod }}{{ if gt (int $v.podsPerInstance) 1 }} --nnodes {{ $v.podsPerInstance }} --node-rank $R --master-addr $MASTER --master-port 29501{{ end }}
{{- end -}}
{{- end -}}

{{/* The container's shell script: start the engine the way .Values.launch says. */}}
{{- define "matrix.script" -}}
R=${HOSTNAME##*-}; case "$R" in ''|*[!0-9]*) R=${LWS_WORKER_INDEX:-0};; esac
MASTER=${LWS_LEADER_ADDRESS:-{{ include "matrix.name" . }}-0.{{ include "matrix.name" . }}-ranks}
{{- $serve := include "matrix.serve" . }}
{{- $rank0 := $serve }}
{{- $other := printf "%s --headless" $serve }}
{{- if eq .Values.engine "sglang" }}{{ $other = $serve }}{{ end }}
{{- if eq .Values.launch "exec" }}
if [ "$R" = 0 ]; then exec {{ $rank0 }}; else exec {{ $other }}; fi
{{- else if eq .Values.launch "setsid" }}
if [ "$R" = 0 ]; then setsid {{ $rank0 }} < /dev/null & else setsid {{ $other }} < /dev/null & fi; wait
{{- else }}
if [ "$R" = 0 ]; then {{ $rank0 }} & else {{ $other }} & fi; wait
{{- end }}
{{- end -}}

{{/* The pod template shared by every kind. */}}
{{- define "matrix.podSpec" -}}
{{- $v := .Values -}}
{{- if $v.ngcImagePullSecretName }}
imagePullSecrets: [{name: {{ $v.ngcImagePullSecretName }}}]
{{- end }}
tolerations: [{key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}]
{{- if gt (int $v.podsPerInstance) 1 }}
affinity:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - {labelSelector: {matchLabels: {app: {{ include "matrix.name" . }}}}, topologyKey: kubernetes.io/hostname}
{{- end }}
{{- with $v.runAsUser }}
securityContext: {runAsUser: {{ . }}, runAsGroup: {{ . }}, fsGroup: {{ . }}}
{{- end }}
containers:
  - name: engine
    image: {{ $v.image }}
    command: ["/bin/bash", "-lc"]
    args:
      - |
        {{- include "matrix.script" . | nindent 8 }}
    env:
      - {name: PYTHONUNBUFFERED, value: "1"}
      - {name: HF_HUB_DISABLE_XET, value: "1"}
      {{- if gt (int $v.podsPerInstance) 1 }}
      - {name: NCCL_MNNVL_ENABLE, value: "0"}
      {{- end }}
      {{- with $v.hfHome }}
      - {name: HF_HOME, value: {{ . | quote }}}
      {{- end }}
      {{- with $v.extraEnv }}
      {{- toYaml . | nindent 6 }}
      {{- end }}
    ports: [{containerPort: {{ $v.port }}, name: http}]
    readinessProbe:
      exec: {command: ["/bin/sh", "-c", "R=${HOSTNAME##*-}; case \"$R\" in ''|*[!0-9]*) R=${LWS_WORKER_INDEX:-0};; esac; if [ \"$R\" = 0 ]; then curl -sf localhost:{{ $v.port }}/v1/models >/dev/null || curl -sf localhost:{{ $v.port }}/health >/dev/null; else pgrep -f \"{{ $v.engine }}\" >/dev/null; fi"]}
      periodSeconds: 5
      failureThreshold: 720
    resources:
      requests: {{- toYaml $v.resources.requests | nindent 8 }}
      limits:
        {{- toYaml $v.resources.limits | nindent 8 }}
        nvidia.com/gpu: {{ $v.gpusPerPod | quote }}
{{- end -}}
