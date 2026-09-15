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
{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "nvcaop.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvcaop.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nvcaop.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "nvcaop.labels" -}}
helm.sh/chart: {{ include "nvcaop.chart" . }}
app.kubernetes.io/name: {{ include "nvcaop.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Base selector labels, to use when defining component selector labels.
*/}}
{{- define "nvcaop.baseSelectorLabels" -}}
app.kubernetes.io/name: {{ include "nvcaop.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ClusterName	truncated at 32 chars
*/}}
{{- define "nvcaop.clustername" -}}
{{- .Values.clustername | trunc 32 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "nvcaop.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "nvcaop.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Validate imagePullSecretName is specified when generateImagePullSecret is false
*/}}
{{- define "nvcaop.validateImagePullSecret" -}}
{{- if not .Values.generateImagePullSecret -}}
{{- if not .Values.imagePullSecretName -}}
{{- fail "imagePullSecretName must be specified when generateImagePullSecret is false" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Reject transport trust settings that explicitly disable QUIC verification.
*/}}
{{- define "nvcaop.validateTransportTrust" -}}
{{- $agentConfig := .Values.agentConfig | default dict -}}
{{- $mergeConfigData := $agentConfig.mergeConfig | default "" -}}
{{- $mergeConfig := dict -}}
{{- if $mergeConfigData -}}
{{- $mergeConfig = $mergeConfigData | fromYaml -}}
{{- end -}}
{{- $mergeWorkload := $mergeConfig.workload | default dict -}}
{{- $mergeTransportTLS := $mergeWorkload.transportTLS | default dict -}}
{{- $operatorConfig := .Values.operatorConfig | default dict -}}
{{- $operatorWorkload := $operatorConfig.workload | default dict -}}
{{- $operatorTransportTLS := $operatorWorkload.transportTLS | default dict -}}
{{- $trustBundle := $operatorTransportTLS.trustBundle | default dict -}}
{{- $secretKeyRef := $trustBundle.secretKeyRef | default dict -}}
{{- $bundleConfigured := or (eq ($mergeTransportTLS.trustMode | default "") "bundle") (ne ($secretKeyRef.name | default "") "") -}}
{{- if and ($mergeWorkload.stargateQUICInsecure | default false) $bundleConfigured -}}
{{- fail "workload.stargateQUICInsecure=true cannot be used with workload.transportTLS.trustMode=bundle; set workload.stargateQUICInsecure=false or use trustMode=system" -}}
{{- end -}}
{{- end -}}

{{/*
Render the effective chart-owned agent configuration. Top-level byoo/utils/
storage/worker values provide the supported API. agentConfig.mergeConfig
remains a legacy override and takes precedence for one minor-version
transition.
*/}}
{{- define "nvcaop.effectiveAgentConfig" -}}
{{- $byoo := .Values.byoo | default dict -}}
{{- $utils := .Values.utils | default dict -}}
{{- $storage := .Values.storage | default dict -}}
{{- $worker := .Values.worker | default dict -}}
{{- $agent := dict -}}
{{- with $byoo.resources }}
{{- $_ := set $agent "BYOOResources" . -}}
{{- end -}}
{{- with $byoo.logChunking }}
{{- $_ := set $agent "byooLogChunking" . -}}
{{- end -}}
{{- with $byoo.otelCollector }}
{{- $_ := set $agent "byooOtelCollector" . -}}
{{- end -}}
{{- with $byoo.additionalResourceOverhead }}
{{- $_ := set $agent "additionalResourceOverhead" . -}}
{{- end -}}
{{- with ($byoo.fluentbit | default dict).resources }}
{{- $_ := set $agent "BYOOFluentBitResources" . -}}
{{- end -}}
{{- with $utils.resources }}
{{- $_ := set $agent "UtilsResources" . -}}
{{- end -}}
{{- $sharedStorage := $storage.sharedStorage | default dict -}}
{{- $sharedStorageServer := dict -}}
{{- with $sharedStorage.server }}
{{- with .image }}
{{- $_ := set $sharedStorageServer "image" . -}}
{{- end -}}
{{- with .resources }}
{{- $_ := set $sharedStorageServer "containerResources" . -}}
{{- end -}}
{{- end -}}
{{- $sharedStorageTaskData := dict -}}
{{- with $sharedStorage.taskData }}
{{- with .storageClassName }}
{{- $_ := set $sharedStorageTaskData "storageClassName" . -}}
{{- end -}}
{{- with .mountOptions }}
{{- $_ := set $sharedStorageTaskData "pvMountOptions" . -}}
{{- end -}}
{{- with .storageCapacity }}
{{- $_ := set $sharedStorageTaskData "storageCapacity" . -}}
{{- end -}}
{{- end -}}
{{- $sharedStorageEffective := dict -}}
{{- if $sharedStorageServer }}
{{- $_ := set $sharedStorageEffective "server" $sharedStorageServer -}}
{{- end -}}
{{- if $sharedStorageTaskData }}
{{- $_ := set $sharedStorageEffective "taskData" $sharedStorageTaskData -}}
{{- end -}}
{{- if $sharedStorageEffective }}
{{- $_ := set $agent "sharedStorage" $sharedStorageEffective -}}
{{- end -}}
{{- $internalPersistentStorage := $storage.internalPersistentStorage | default dict -}}
{{- $ipsEffective := dict -}}
{{- with $internalPersistentStorage.storageClassName }}
{{- $_ := set $ipsEffective "storageClassName" . -}}
{{- end -}}
{{- with $internalPersistentStorage.hardResourceQuota }}
{{- $_ := set $ipsEffective "hardResourceQuota" . -}}
{{- end -}}
{{- if $ipsEffective }}
{{- $_ := set $agent "internalPersistentStorage" $ipsEffective -}}
{{- end -}}
{{- with $worker.minHealthcheckRefreshWait }}
{{- $_ := set $agent "minHealthcheckRefreshWait" . -}}
{{- end -}}
{{- with $worker.staticGPUCapacity }}
{{- $_ := set $agent "staticGPUCapacity" . -}}
{{- end -}}
{{- with $worker.computeBackend }}
{{- $_ := set $agent "computeBackend" . -}}
{{- end -}}
{{- with $worker.requestsNamespace }}
{{- $_ := set $agent "requestsNamespace" . -}}
{{- end -}}
{{- with $worker.namespaceLabels }}
{{- $_ := set $agent "namespaceLabels" . -}}
{{- end -}}
{{- with $worker.featureFlags }}
{{- $_ := set $agent "featureFlags" . -}}
{{- end -}}
{{- with $worker.skipSelfDestruct }}
{{- $_ := set $agent "skipSelfDestruct" . -}}
{{- end -}}
{{- with $worker.forceSelfDestruct }}
{{- $_ := set $agent "forceSelfDestruct" . -}}
{{- end -}}
{{- with $worker.csiVolumeMountOptions }}
{{- $_ := set $agent "csiVolumeMountOptions" . -}}
{{- end -}}
{{- $timeouts := $worker.timeouts | default dict -}}
{{- with $timeouts.credRenewInterval }}
{{- $_ := set $agent "credRenewInterval" . -}}
{{- end -}}
{{- with $timeouts.heartbeatInterval }}
{{- $_ := set $agent "heartbeatInterval" . -}}
{{- end -}}
{{- with $timeouts.syncQueueInterval }}
{{- $_ := set $agent "syncQueueInterval" . -}}
{{- end -}}
{{- with $timeouts.syncRequestStatusInterval }}
{{- $_ := set $agent "syncRequestStatusInterval" . -}}
{{- end -}}
{{- with $timeouts.syncAcknowledgeRequestInterval }}
{{- $_ := set $agent "syncAcknowledgeRequestInterval" . -}}
{{- end -}}
{{- with $timeouts.periodicInstanceStatusInterval }}
{{- $_ := set $agent "periodicInstanceStatusInterval" . -}}
{{- end -}}
{{- with $timeouts.icmsRequestAckInterval }}
{{- $_ := set $agent "icmsRequestAckInterval" . -}}
{{- end -}}
{{- with $timeouts.icmsRequestAckRetryTimeout }}
{{- $_ := set $agent "icmsRequestAckRetryTimeout" . -}}
{{- end -}}
{{- $config := dict -}}
{{- if $agent }}
{{- $_ := set $config "agent" $agent -}}
{{- end -}}
{{- $agentConfig := .Values.agentConfig | default dict -}}
{{- $mergeConfigData := $agentConfig.mergeConfig | default "" -}}
{{- if $mergeConfigData }}
{{- $parsedMergeConfig := $mergeConfigData | fromYaml -}}
{{- if hasKey $parsedMergeConfig "Error" -}}
{{- fail (printf "agentConfig.mergeConfig contains invalid YAML: %s" $parsedMergeConfig.Error) -}}
{{- end -}}
{{- $config = mergeOverwrite $config ($parsedMergeConfig | default dict) -}}
{{- end -}}
{{- $finalAgent := $config.agent | default dict -}}
{{- if and $finalAgent.skipSelfDestruct $finalAgent.forceSelfDestruct -}}
{{- fail "worker.skipSelfDestruct and worker.forceSelfDestruct cannot both be true (including via agentConfig.mergeConfig); NVCA rejects this combination at startup" -}}
{{- end -}}
{{- $config | toYaml -}}
{{- end -}}

{{/*
Keys under agentConfig.mergeConfig's "agent" map that now have first-class
chart values. Used to detect legacy overrides so the operator can emit a
source-aware migration warning.
*/}}
{{- define "nvcaop.firstClassAgentConfigKeys" -}}
{{- list "additionalResourceOverhead" "UtilsResources" "sharedStorage" "internalPersistentStorage" "minHealthcheckRefreshWait" "staticGPUCapacity" "computeBackend" "requestsNamespace" "namespaceLabels" "featureFlags" "skipSelfDestruct" "forceSelfDestruct" "csiVolumeMountOptions" "credRenewInterval" "heartbeatInterval" "syncQueueInterval" "syncRequestStatusInterval" "syncAcknowledgeRequestInterval" "periodicInstanceStatusInterval" "icmsRequestAckInterval" "icmsRequestAckRetryTimeout" | toYaml -}}
{{- end -}}

{{/*
Return true when legacy agentConfig.mergeConfig configures any field that now
has a first-class chart value (BYOO, utils, storage, or worker). The ConfigMap
annotation lets the operator emit a source-aware migration warning.
*/}}
{{- define "nvcaop.usesLegacyFirstClassConfig" -}}
{{- $agentConfig := .Values.agentConfig | default dict -}}
{{- $mergeConfigData := $agentConfig.mergeConfig | default "" -}}
{{- $legacyFirstClass := false -}}
{{- if $mergeConfigData }}
{{- $firstClassKeys := include "nvcaop.firstClassAgentConfigKeys" . | fromYamlArray -}}
{{- $config := $mergeConfigData | fromYaml | default dict -}}
{{- $agent := $config.agent | default dict -}}
{{- range $key, $_ := $agent }}
{{- if or (hasPrefix "byoo" (lower $key)) (has $key $firstClassKeys) }}
{{- $legacyFirstClass = true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $legacyFirstClass -}}
{{- end -}}

{{/*
ImagePullSecret for images.
*/}}
{{- define "nvcaop.generatedImagePullSecret" }}
{{- $username := .Values.ngcConfig.username }}
{{- $serviceKey := .Values.ngcConfig.serviceKey | required "NGC service key is required to create a pull secret" }}
{{- $auths := dict }}
{{- $auth := dict "username" $username "password" $serviceKey "auth" (printf "%s:%s" $username $serviceKey | b64enc) }}
{{- range $i, $repo := (list .Values.image.repository .Values.nvcaImage.repositoryOverride) }}
{{- if $repo }}
{{- $_ := set $auths (splitList "/" $repo | first) $auth }}
{{- end }}
{{- end }}
{{- printf "{\"auths\":%s}" ($auths | toJson) | b64enc }}
{{- end }}

{{/*
Convert list to JSON if non-empty otherwise return []
Usage: {{ include "nvcaop.jsonListOrEmpty" <list> }}
*/}}
{{- define "nvcaop.jsonListOrEmpty" -}}
{{- $l := (default (list) .) | compact -}}
{{- if gt (len $l) 0 -}}
{{ $l | toJson }}
{{- else -}}
[]
{{- end -}}
{{- end -}}

{{/*
Get the imageCredHelper repository based on image.repository
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.imageCredHelperRepository" (dict "imageRepository" .Values.helmManaged.imageCredHelper.imageRepository "defaultRepository" .Values.image.repository) }}
*/}}
{{- define "nvcaop.imageCredHelperRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper
{{- end -}}
{{- end -}}

{{/*
Get the OTel collector repository based on image.repository
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.otelCollectorRepository" (dict "imageRepository" .Values.otelCollector.imageRepository "defaultRepository" .Values.image.repository) }}
*/}}
{{- define "nvcaop.otelCollectorRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector
{{- end -}}
{{- end -}}

{{/*
Get the BYOO OTel collector repository based on image.repository.
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.byooOtelCollectorImage" . }}
*/}}
{{- define "nvcaop.byooOtelCollectorRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nv-cf/nvcf-core/byoo-otel-collector
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector
{{- end -}}
{{- end -}}

{{/*
Get the BYOO OTel collector image when its tag is configured.
*/}}
{{- define "nvcaop.byooOtelCollectorImage" -}}
{{- $agent := .Values.agent | default dict -}}
{{- $byooOtelCollector := $agent.byooOtelCollector | default dict -}}
{{- $imageTag := "0.157.0-nv-0.2.1" -}}
{{- if hasKey $byooOtelCollector "imageTag" -}}
{{- $imageTag = $byooOtelCollector.imageTag -}}
{{- end -}}
{{- if $imageTag -}}
{{- printf "%s:%s" (include "nvcaop.byooOtelCollectorRepository" (dict "imageRepository" ($byooOtelCollector.imageRepository | default "") "defaultRepository" .Values.image.repository)) $imageTag -}}
{{- end -}}
{{- end -}}

{{/*
Check if cluster validator is enabled (nil-safe).
Returns non-empty string if enabled, empty string if disabled.
Usage: {{- if (include "nvcaop.clusterValidatorEnabled" .) -}}
*/}}
{{- define "nvcaop.clusterValidatorEnabled" -}}
{{- $cv := .Values.clusterValidator | default dict -}}
{{- if ($cv.enabled | default false) -}}true{{- end -}}
{{- end -}}

{{/*
Cluster validator config with chart defaults merged in.
Returns YAML; callers `| fromYaml` it and dereference safely.

Defends against `helm upgrade --reuse-values` from a release created before
clusterValidator existed: in that case the stored values have no
clusterValidator.image / .resources / .schedule sections, and Helm does not
fall back to the new chart's values.yaml defaults for absent keys. Without
this helper, deployment.yaml / cronjob.yaml hit a nil pointer when
dereferencing .Values.clusterValidator.image.repository.

`merge` keeps existing user values when present and only fills in defaults
for absent keys, so explicit overrides are preserved.

Usage: {{- $cv := include "nvcaop.clusterValidatorConfig" . | fromYaml -}}
*/}}
{{- define "nvcaop.clusterValidatorConfig" -}}
{{- /* Defaults must mirror values.yaml so a --reuse-values upgrade gets
       exactly the same effective config as a fresh install. */ -}}
{{- $defaults := dict
    "image" (dict "repository" "" "tag" "" "pullPolicy" "IfNotPresent")
    "schedule" "0 */3 * * *"
    "configMapName" "cluster-validator-network-checks"
    "networkChecks" (dict)
    "resources" (dict
      "requests" (dict "cpu" "100m" "memory" "64Mi")
      "limits"   (dict "cpu" "200m" "memory" "128Mi"))
-}}
{{- /* `merge (dict) user defaults` writes into a fresh empty dict so
       .Values.clusterValidator is never mutated in-place. Sprig's
       `merge dst src...` modifies dst; if dst were $cv directly, the
       default keys would be written back into .Values.clusterValidator
       and any other template that reads .Values directly after the
       first render would see merged-in defaults instead of original
       user values. */ -}}
{{- $user := .Values.clusterValidator | default dict -}}
{{- merge (dict) $user $defaults | toYaml -}}
{{- end -}}

{{/*
Get the cluster-validator repository based on image.repository
If imageRepository is explicitly set, use it. Otherwise, calculate it based on image.repository prefix.
Usage: {{ include "nvcaop.clusterValidatorRepository" (dict "imageRepository" .Values.clusterValidator.image.repository "defaultRepository" .Values.image.repository) }}
*/}}
{{- define "nvcaop.clusterValidatorRepository" -}}
{{- if .imageRepository -}}
{{- .imageRepository -}}
{{- else if hasPrefix "stg.nvcr.io/nvidia/nvcf-byoc" .defaultRepository -}}
stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator
{{- else -}}
nvcr.io/nvidia/nvcf-byoc/cluster-validator
{{- end -}}
{{- end -}}
