{{/* SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0 */}}

{{- define "matrix.name" -}}{{ .Values.helmChartServiceName }}{{- end -}}

{{/* The engine's command line, for this pod's rank ($R in the shell). */}}
{{- define "matrix.serve" -}}
{{- $v := .Values -}}
{{- if eq $v.engine "command" -}}
{{ required "engine command needs .Values.command" $v.command }}{{ with $v.extraArgs }} {{ . }}{{ end }}
{{- else if eq $v.engine "sglang" -}}
python3 -m sglang.launch_server --model-path {{ $v.model }}{{ with $v.servedModelName }} --served-model-name {{ . }}{{ end }} --host 0.0.0.0 --port {{ $v.port }} --context-length {{ $v.maxModelLen }} --mem-fraction-static {{ $v.gpuMemoryUtilization }} --tp {{ mul $v.podsPerInstance $v.gpusPerPod }}{{ if gt (int $v.podsPerInstance) 1 }} --nnodes {{ $v.podsPerInstance }} --node-rank $R --dist-init-addr $MASTER:5000{{ end }}{{ with $v.extraArgs }} {{ . }}{{ end }}
{{- else -}}
vllm serve {{ $v.model }}{{ with $v.servedModelName }} --served-model-name {{ . }}{{ end }} --host 0.0.0.0 --port {{ $v.port }} --max-model-len {{ $v.maxModelLen }} --gpu-memory-utilization {{ $v.gpuMemoryUtilization }} --tensor-parallel-size {{ mul $v.podsPerInstance $v.gpusPerPod }}{{ if gt (int $v.podsPerInstance) 1 }} --nnodes {{ $v.podsPerInstance }} --node-rank $R --master-addr $MASTER --master-port 29501{{ end }}{{ with $v.extraArgs }} {{ . }}{{ end }}
{{- end -}}
{{- end -}}

{{/* The container's shell script: start the engine the way .Values.launch says. */}}
{{- define "matrix.script" -}}
# NVCF mounts the function's secrets here; export the ones engines read.
if [ -f /var/secrets/secrets.json ] && command -v python3 >/dev/null; then
  eval "$(python3 -c 'import json,shlex; d=json.load(open("/var/secrets/secrets.json")); [print("export %s=%s" % (k, shlex.quote(str(d[k])))) for k in ("HF_TOKEN","NGC_API_KEY") if d.get(k)]' 2>/dev/null)"
fi
R=${HOSTNAME##*-}; case "$R" in ''|*[!0-9]*) R=${LWS_WORKER_INDEX:-0};; esac
MASTER=${LWS_LEADER_ADDRESS:-{{ include "matrix.name" . }}-0.{{ include "matrix.name" . }}-ranks}
{{- $serve := include "matrix.serve" . }}
{{- $rank0 := $serve }}
{{- $other := $serve }}
{{- if eq .Values.engine "vllm" }}{{ $other = printf "%s --headless" $serve }}{{ end }}
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
      - {name: NCCL_MNNVL_ENABLE, value: {{ ternary "1" "0" (and $v.computeDomain true) | quote }}}
      {{- end }}
      {{- with $v.hfHome }}
      - {name: HF_HOME, value: {{ . | quote }}}
      {{- end }}
      {{- with $v.extraEnv }}
      {{- toYaml . | nindent 6 }}
      {{- end }}
    ports: [{containerPort: {{ $v.port }}, name: http}]
    readinessProbe:
      # Every rank is ready when rank 0 serves: a rank that lost its peers
      # must not look ready.
      exec: {command: ["/bin/sh", "-c", "R=${HOSTNAME##*-}; case \"$R\" in ''|*[!0-9]*) R=${LWS_WORKER_INDEX:-0};; esac; H=localhost; [ \"$R\" = 0 ] || H=${LWS_LEADER_ADDRESS:-{{ include "matrix.name" . }}-0.{{ include "matrix.name" . }}-ranks}; {{ if $v.healthPath }}curl -sf $H:{{ $v.port }}{{ $v.healthPath }} >/dev/null{{ else }}curl -sf $H:{{ $v.port }}/v1/models >/dev/null || curl -sf $H:{{ $v.port }}/health >/dev/null{{ end }}"]}
      periodSeconds: 5
      failureThreshold: 720
    # NCCL keeps its buffers in /dev/shm; the container default of 64 MiB
    # does not fit even two ranks.
    volumeMounts: [{name: dshm, mountPath: /dev/shm}]
    resources:
      requests: {{- toYaml $v.resources.requests | nindent 8 }}
      limits:
        {{- toYaml $v.resources.limits | nindent 8 }}
        nvidia.com/gpu: {{ $v.gpusPerPod | quote }}
      {{- if $v.computeDomain }}
      claims: [{name: imex-channel}]
      {{- end }}
{{- if $v.computeDomain }}
resourceClaims:
  - {name: imex-channel, resourceClaimTemplateName: {{ include "matrix.name" . }}-imex-channel}
{{- end }}
volumes:
  - {name: dshm, emptyDir: {medium: Memory, sizeLimit: {{ $v.shmSize }}}}
{{- end -}}
