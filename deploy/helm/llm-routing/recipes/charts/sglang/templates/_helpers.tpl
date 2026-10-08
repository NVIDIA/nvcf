{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{- define "sglang.settings" -}}
{{- if eq .Release.Name "llm-stack" }}{{ fail "llm-stack is reserved for the shared stack; use a separate model release name" }}{{ end -}}
{{- $v := deepCopy .Values -}}
{{- if not (has $v.mode (list "automatic" "phased")) }}{{ fail "mode must be automatic or phased" }}{{ end -}}
{{- if and (eq $v.mode "phased") (or (empty $v.phase) (eq $v.phase "deploy")) }}{{ fail "phased mode requires an explicit phase" }}{{ end -}}
{{- $automatic := or (empty $v.phase) (eq $v.phase "deploy") -}}
{{- $_ := set $v "automatic" $automatic -}}
{{- if $automatic -}}
{{- $recipes := .Files.Get "files/profiles.json" | fromJson -}}
{{- $model := get $recipes $v.recipe -}}
{{- if not $model }}{{ fail "Automatic mode requires a bundled recipe" }}{{ end -}}
{{- $profile := index $model.profiles 0 -}}
{{- if $v.profileName -}}
{{- $matches := list -}}
{{- range $candidate := $model.profiles }}{{ if eq $candidate.id $v.profileName }}{{ $matches = append $matches $candidate }}{{ end }}{{ end -}}
{{- if ne (len $matches) 1 }}{{ fail "profileName does not belong to the selected recipe" }}{{ end -}}
{{- $profile = index $matches 0 -}}
{{- else if gt (len $model.profiles) 1 }}{{ fail "profileName is required for a recipe with multiple profiles" }}{{ end -}}
{{- if ne (len $v.nodes) (int $profile.nodes) }}{{ fail "nodes must contain exactly one explicit node per profile rank" }}{{ end -}}
{{- if ne (len (uniq $v.nodes)) (len $v.nodes) }}{{ fail "Each rank needs a distinct node" }}{{ end -}}
{{- $targets := list -}}
{{- range $rank, $node := $v.nodes -}}
{{- if or (not (kindIs "string" $node)) (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $node)) }}{{ fail "nodes must contain a Kubernetes node name" }}{{ end -}}
{{- $facts := default dict (get $v.nodeCapabilities $node) -}}
{{- $target := dict "node" $node -}}
{{- if $profile.offload -}}
{{- if not (and (kindIs "bool" $facts.localNvme) $facts.localNvme) }}{{ fail "NVMe offload requires nodeCapabilities for the selected node with localNvme=true" }}{{ end -}}
{{- $_ := set $target "localNvme" true -}}
{{- end -}}
{{- if $profile.fabric -}}
{{- $_ := required "TP2 requires nodeCapabilities.fabric for every selected node" $facts.fabric -}}
{{- $_ := required "TP2 requires nodeCapabilities.address for every selected node" $facts.address -}}
{{- if not (regexMatch "^[a-zA-Z0-9_.-]{1,15}$" (default "" $facts.interface)) }}{{ fail "TP2 requires a valid nodeCapabilities.interface" }}{{ end -}}
{{- if lt (float64 (default 0 $facts.linkGbps)) (float64 $profile.minFabricGbps) }}{{ fail "TP2 nodeCapabilities.linkGbps is below the profile minimum" }}{{ end -}}
{{- $_ := merge $target (pick $facts "fabric" "address" "interface" "linkGbps") -}}
{{- end -}}
{{- $targets = append $targets $target -}}
{{- end -}}
{{- if $profile.fabric -}}
{{- $ports := list $v.ports.http $v.ports.bootstrap $v.ports.rendezvous -}}
{{- if ne (len (uniq $ports)) 3 }}{{ fail "TP2 HTTP, bootstrap and rendezvous ports must be distinct" }}{{ end -}}
{{- range $port := $ports }}{{ if or (lt (int $port) 1) (gt (int $port) 65535) }}{{ fail "TP2 ports must be between 1 and 65535" }}{{ end }}{{ end -}}
{{- if ne (index $targets 0).fabric (index $targets 1).fabric }}{{ fail "TP2 targets must share the same verified fabric" }}{{ end -}}
{{- if eq (index $targets 0).address (index $targets 1).address }}{{ fail "TP2 targets require distinct fabric addresses" }}{{ end -}}
{{- end -}}
{{- $_ := required "sharedCAConfigMap is required for automatic mode" $v.sharedCAConfigMap -}}
{{- $_ := required "storageClassName is required for automatic mode" $v.storageClassName -}}
{{- $_ := set $v "model" (omit $model "profiles") -}}
{{- $_ := set $v "profile" $profile -}}
{{- $_ := set $v "image" $model.image -}}
{{- $_ := set $v "gpu" (dict "product" (index $profile.hardware.gpuProducts 0)) -}}
{{- $_ := set $v "targets" $targets -}}
{{- $_ := set $v "phase" "serve" -}}
{{- if and $v.cache.existingClaim (gt (len $v.nodes) 1) }}{{ fail "TP2 retained caches require cache.existingClaims in rank order" }}{{ end -}}
{{- if and $v.cache.existingClaim $v.cache.existingClaims }}{{ fail "Use either cache.existingClaim or cache.existingClaims" }}{{ end -}}
{{- if and $v.cache.existingClaims (ne (len $v.cache.existingClaims) (len $v.nodes)) }}{{ fail "cache.existingClaims must match the number of ranks" }}{{ end -}}
{{- $claims := list -}}
{{- range $rank, $target := $targets -}}
{{- $claim := "" -}}
{{- if $v.cache.existingClaims }}{{ $claim = index $v.cache.existingClaims $rank }}{{ else if $v.cache.existingClaim }}{{ $claim = $v.cache.existingClaim }}{{ end -}}
{{- if and $claim (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $claim)) }}{{ fail "cache.existingClaim(s) must contain a PVC name" }}{{ end -}}
{{- $claims = append $claims $claim -}}
{{- end -}}
{{- if and (eq (len $claims) 2) (index $claims 0) (eq (index $claims 0) (index $claims 1)) }}{{ fail "Each rank needs a distinct cache claim" }}{{ end -}}
{{- $_ := set $v "existingClaims" $claims -}}
{{- end -}}
{{- $v | toJson -}}
{{- end -}}

{{- define "sglang.nodeRole" -}}
{{- printf "%s-node-%s" (.Release.Name | trunc 35 | trimSuffix "-") (.Release.Namespace | sha256sum | trunc 12) -}}
{{- end -}}
