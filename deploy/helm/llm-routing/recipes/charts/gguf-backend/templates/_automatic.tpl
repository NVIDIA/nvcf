{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{- define "gguf.automatic" -}}
{{- $v := deepCopy .Values -}}
{{- $base := printf "files/recipes/%s/" $v.recipe -}}
{{- $metadata := .Files.Get (printf "%sprofiles.json" $base) | fromJson -}}
{{- if not $metadata }}{{ fail "Automatic mode supports the bundled glm-5.3 recipe" }}{{ end -}}
{{- $profile := index $metadata.profiles 0 -}}
{{- if $v.profileName -}}
{{- $matches := list -}}
{{- range $candidate := $metadata.profiles }}{{ if eq $candidate.id $v.profileName }}{{ $matches = append $matches $candidate }}{{ end }}{{ end -}}
{{- if ne (len $matches) 1 }}{{ fail "profileName does not belong to the selected recipe" }}{{ end -}}
{{- $profile = index $matches 0 -}}
{{- end -}}
{{- if ne (len $v.nodes) (int $profile.nodes) }}{{ fail (printf "Automatic GLM profile %s requires exactly %d explicit nodes" $profile.id (int $profile.nodes)) }}{{ end -}}
{{- if ne (len (uniq $v.nodes)) (len $v.nodes) }}{{ fail "Automatic GLM nodes must be distinct" }}{{ end -}}
{{- range $node := $v.nodes }}{{- if or (not (kindIs "string" $node)) (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $node)) }}{{ fail "nodes must contain Kubernetes node names" }}{{ end }}{{ end -}}
{{- $_ := required "sharedCAConfigMap is required for automatic mode" $v.sharedCAConfigMap -}}
{{- $_ := required "storageClassName is required for automatic mode" $v.storageClassName -}}
{{- $_ := required "runtimeClassName is required for automatic mode" $v.runtimeClassName -}}
{{- if not (kindIs "bool" $v.reuseCaches) }}{{ fail "reuseCaches must be a boolean" }}{{ end -}}
{{- $workers := int (sub (len $profile.roles) 1) -}}
{{- $workerExisting := list -}}
{{- if $v.rpc.cache.existingClaims -}}
{{- if $v.rpc.cache.existingClaim }}{{ fail "Use either rpc.cache.existingClaim or rpc.cache.existingClaims" }}{{ end -}}
{{- if ne (len $v.rpc.cache.existingClaims) $workers }}{{ fail "rpc.cache.existingClaims must list one claim per RPC worker" }}{{ end -}}
{{- $workerExisting = $v.rpc.cache.existingClaims -}}
{{- else if $v.rpc.cache.existingClaim -}}
{{- if ne $workers 1 }}{{ fail "rpc.cache.existingClaim serves one RPC worker; use rpc.cache.existingClaims for several" }}{{ end -}}
{{- $workerExisting = list $v.rpc.cache.existingClaim -}}
{{- else -}}
{{- range $i := until $workers }}{{ $workerExisting = append $workerExisting "" }}{{ end -}}
{{- end -}}
{{- $existing := concat (list (default "" $v.artifacts.existingClaim)) $workerExisting -}}
{{- range $claim := $existing }}{{- if and $claim (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $claim)) }}{{ fail "Existing cache claims must be PVC names" }}{{ end }}{{ end -}}
{{- $named := without $existing "" -}}
{{- if ne (len (uniq $named)) (len $named) }}{{ fail "Model and RPC caches require distinct PVCs" }}{{ end -}}
{{- if and $v.reuseCaches (ne (len $named) (len $existing)) }}{{ fail "reuseCaches requires artifacts.existingClaim and rpc.cache.existingClaim (or existingClaims)" }}{{ end -}}
{{- $recipe := .Files.Get (printf "%srecipe.json" $base) | fromJson -}}
{{- $tuning := required "Recipe tuning must define the profile memory mode" (index $recipe.tuning $profile.hardware.memoryMode) -}}
{{- if or (ne (int $profile.contextLength) (int $tuning.contextPerSlot)) (ne (int $profile.concurrency) (int $tuning.slots)) }}{{ fail "Automatic profile context and concurrency must match recipe tuning" }}{{ end -}}
{{- $lock := .Files.Get (printf "%smodel.lock.json" $base) | fromJson -}}
{{- $image := $metadata.runtimeImage -}}
{{- if and $v.image (ne $v.image $image) }}{{ fail "Automatic runtime image is pinned by the recipe profile" }}{{ end -}}
{{- $runtimeProfile := omit $profile "validation" "validatedWorkload" "automaticHelmValidation" "qualification" "artifactEvidence" -}}
{{- $claims := list (default (printf "%s-artifacts" .Release.Name) $v.artifacts.existingClaim) -}}
{{- $endpoints := list -}}
{{- $devices := list "CUDA0" -}}
{{- range $i, $role := slice $profile.roles 1 }}
{{- $claims = append $claims (default (printf "%s-rpc-cache-%s" $.Release.Name $role.id) (index $workerExisting $i)) -}}
{{- $endpoints = append $endpoints (printf "%s-rpc-%s:50052" $.Release.Name $role.id) -}}
{{- $devices = append $devices (printf "RPC%d" $i) -}}
{{- end -}}
{{- dict "recipe" $recipe "tuning" $tuning "lock" $lock "profile" $runtimeProfile "image" $image "reuseCaches" $v.reuseCaches "claims" $claims "existing" $existing "devices" (join "," $devices) "runtimeURL" (printf "http://%s-artifacts:8080/runtime.tar.gz" .Release.Name) "rpcEndpoints" $endpoints | toJson -}}
{{- end -}}

{{- define "gguf.nodeRole" -}}
{{- printf "%s-node-%s" (.Release.Name | trunc 35 | trimSuffix "-") (.Release.Namespace | sha256sum | trunc 12) -}}
{{- end -}}
