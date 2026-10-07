{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{- define "sglang.settings" -}}
{{- $v := deepCopy .Values -}}
{{- if not (has $v.mode (list "automatic" "phased")) }}{{ fail "mode must be automatic or phased" }}{{ end -}}
{{- if and (eq $v.mode "phased") (or (empty $v.phase) (eq $v.phase "deploy")) }}{{ fail "phased mode requires an explicit phase" }}{{ end -}}
{{- $automatic := or (empty $v.phase) (eq $v.phase "deploy") -}}
{{- $_ := set $v "automatic" $automatic -}}
{{- if $automatic -}}
{{- $recipes := .Files.Get "files/profiles.json" | fromJson -}}
{{- $model := get $recipes $v.recipe -}}
{{- if not $model }}{{ fail "Automatic mode supports qwen3.8-27b and qwen3.8-27b-nvfp4" }}{{ end -}}
{{- $profile := index $model.profiles 0 -}}
{{- if and $v.profileName (ne $v.profileName $profile.id) }}{{ fail "profileName does not belong to the selected recipe" }}{{ end -}}
{{- if ne (len $v.nodes) 1 }}{{ fail "Automatic profiles require exactly one explicit node in nodes" }}{{ end -}}
{{- $node := index $v.nodes 0 -}}
{{- if or (not (kindIs "string" $node)) (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $node)) }}{{ fail "nodes must contain a Kubernetes node name" }}{{ end -}}
{{- $_ := required "sharedCAConfigMap is required for automatic mode" $v.sharedCAConfigMap -}}
{{- $_ := required "storageClassName is required for automatic mode" $v.storageClassName -}}
{{- $_ := set $v "model" (omit $model "profiles") -}}
{{- $_ := set $v "profile" $profile -}}
{{- $_ := set $v "image" $model.image -}}
{{- $_ := set $v "gpu" (dict "product" (index $profile.hardware.gpuProducts 0)) -}}
{{- $_ := set $v "targets" (list (dict "node" $node)) -}}
{{- $_ := set $v "phase" "serve" -}}
{{- if and $v.cache.existingClaim (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $v.cache.existingClaim)) }}{{ fail "cache.existingClaim must be a PVC name" }}{{ end -}}
{{- end -}}
{{- $v | toJson -}}
{{- end -}}

{{- define "sglang.nodeRole" -}}
{{- printf "%s-node-%s" (.Release.Name | trunc 35 | trimSuffix "-") (.Release.Namespace | sha256sum | trunc 12) -}}
{{- end -}}
