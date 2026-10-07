{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{- define "gguf.automatic" -}}
{{- $v := deepCopy .Values -}}
{{- $base := printf "files/recipes/%s/" $v.recipe -}}
{{- $metadata := .Files.Get (printf "%sprofiles.json" $base) | fromJson -}}
{{- if not $metadata }}{{ fail "Automatic mode supports the bundled glm-5.3 recipe" }}{{ end -}}
{{- $profile := index $metadata.profiles 0 -}}
{{- if and $v.profileName (ne $v.profileName $profile.id) }}{{ fail "profileName does not belong to the selected recipe" }}{{ end -}}
{{- if ne (len $v.nodes) (int $profile.nodes) }}{{ fail "Automatic GLM profile requires exactly two explicit nodes" }}{{ end -}}
{{- if eq (index $v.nodes 0) (index $v.nodes 1) }}{{ fail "Automatic GLM nodes must be distinct" }}{{ end -}}
{{- range $node := $v.nodes }}{{- if or (not (kindIs "string" $node)) (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $node)) }}{{ fail "nodes must contain Kubernetes node names" }}{{ end }}{{ end -}}
{{- $_ := required "sharedCAConfigMap is required for automatic mode" $v.sharedCAConfigMap -}}
{{- $_ := required "storageClassName is required for automatic mode" $v.storageClassName -}}
{{- $_ := required "runtimeClassName is required for automatic mode" $v.runtimeClassName -}}
{{- if not (kindIs "bool" $v.reuseCaches) }}{{ fail "reuseCaches must be a boolean" }}{{ end -}}
{{- range $claim := list $v.artifacts.existingClaim $v.rpc.cache.existingClaim }}{{- if and $claim (not (regexMatch "^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$" $claim)) }}{{ fail "Existing cache claims must be PVC names" }}{{ end }}{{ end -}}
{{- if and $v.artifacts.existingClaim (eq $v.artifacts.existingClaim $v.rpc.cache.existingClaim) }}{{ fail "Model and RPC caches require distinct PVCs" }}{{ end -}}
{{- if and $v.reuseCaches (or (not $v.artifacts.existingClaim) (not $v.rpc.cache.existingClaim)) }}{{ fail "reuseCaches requires artifacts.existingClaim and rpc.cache.existingClaim" }}{{ end -}}
{{- $recipe := .Files.Get (printf "%srecipe.json" $base) | fromJson -}}
{{- $lock := .Files.Get (printf "%smodel.lock.json" $base) | fromJson -}}
{{- $image := $metadata.runtimeImage -}}
{{- if and $v.image (ne $v.image $image) }}{{ fail "Automatic runtime image is pinned by the recipe profile" }}{{ end -}}
{{- $leaderClaim := default (printf "%s-artifacts" .Release.Name) $v.artifacts.existingClaim -}}
{{- $workerClaim := default (printf "%s-rpc-cache-n1" .Release.Name) $v.rpc.cache.existingClaim -}}
{{- dict "recipe" $recipe "lock" $lock "profile" $profile "image" $image "reuseCaches" $v.reuseCaches "claims" (list $leaderClaim $workerClaim) "runtimeURL" (printf "http://%s-artifacts:8080/runtime.tar.gz" .Release.Name) "rpcEndpoints" (list (printf "%s-rpc-n1:50052" .Release.Name)) | toJson -}}
{{- end -}}

{{- define "gguf.nodeRole" -}}
{{- printf "%s-node-%s" (.Release.Name | trunc 35 | trimSuffix "-") (.Release.Namespace | sha256sum | trunc 12) -}}
{{- end -}}
