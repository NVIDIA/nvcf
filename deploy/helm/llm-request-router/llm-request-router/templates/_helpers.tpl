{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/}}

{{- define "llm-request-router.name" -}}
{{- default .Chart.Name .Values.llmRequestRouter.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "llm-request-router.fullname" -}}
{{- if .Values.llmRequestRouter.fullnameOverride }}
{{- .Values.llmRequestRouter.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- include "llm-request-router.name" . }}
{{- end }}
{{- end }}

{{- define "llm-request-router.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "llm-request-router.labels" -}}
helm.sh/chart: {{ include "llm-request-router.chart" . }}
{{ include "llm-request-router.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "llm-request-router.selectorLabels" -}}
app.kubernetes.io/name: {{ include "llm-request-router.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "llm-request-router.selectorLabelSelector" -}}
{{- printf "app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s" (include "llm-request-router.name" .) .Release.Name -}}
{{- end }}

{{- define "llm-request-router.backendRouterName" -}}
{{- printf "%s-backend-router" (include "llm-request-router.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "llm-request-router.backendRouterSelectorLabels" -}}
app.kubernetes.io/name: {{ include "llm-request-router.backendRouterName" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "llm-request-router.backendRouterImageTag" -}}
{{- $image := .Values.llmRequestRouter.backendRouter.image -}}
{{- $mainTag := default .Chart.AppVersion .Values.llmRequestRouter.image.tag -}}
{{- default $mainTag $image.tag -}}
{{- end }}

{{- define "llm-request-router.backendRouterLabels" -}}
helm.sh/chart: {{ include "llm-request-router.chart" . }}
{{ include "llm-request-router.backendRouterSelectorLabels" . }}
app.kubernetes.io/component: backend-router
app.kubernetes.io/version: {{ include "llm-request-router.backendRouterImageTag" . | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "llm-request-router.namespace" -}}
{{- default .Release.Namespace .Values.llmRequestRouter.namespace -}}
{{- end -}}

{{- define "llm-request-router.workloadKind" -}}
{{- dig "workload" "kind" "Deployment" .Values.llmRequestRouter | toString -}}
{{- end -}}

{{- define "llm-request-router.isExplicitHttpUri" -}}
{{- $uri := . | toString | trim -}}
{{- $authorityValid := regexMatch "^https?://([A-Za-z0-9._~-]+|\\[[0-9A-Fa-f:.]+\\])(:[0-9]+)?/?$" $uri -}}
{{- $portMatch := regexFind ":[0-9]+/?$" $uri -}}
{{- $portText := $portMatch | trimPrefix ":" | trimSuffix "/" -}}
{{- $portValid := or
      (not $portMatch)
      (and (le (len $portText) 5) (le ($portText | int) 65535)) -}}
{{- if and $authorityValid $portValid -}}true{{- end -}}
{{- end -}}

{{- define "llm-request-router.validateRemoteWatchUrls" -}}
{{- $discovery := .Values.llmRequestRouter.discovery | default dict -}}
{{- $allowHttp := dig "allowInsecureRemoteWatchHttp" false $discovery -}}
{{- range $remoteWatchUrl := dig "remoteWatchUrls" (list) $discovery -}}
{{- $remoteWatchUrl = $remoteWatchUrl | toString | trim -}}
{{- if ne (include "llm-request-router.isExplicitHttpUri" $remoteWatchUrl) "true" -}}
{{- fail "llmRequestRouter.discovery.remoteWatchUrls entries must be explicit http:// or https:// URIs" -}}
{{- end -}}
{{- if and (hasPrefix "http://" $remoteWatchUrl) (not $allowHttp) -}}
{{- fail "llmRequestRouter.discovery.remoteWatchUrls requires https://; set allowInsecureRemoteWatchHttp=true only for development plaintext endpoints" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
An unset backendRouter.enabled follows the workload contract: a multi-replica
Deployment needs the EndpointSlice router, while StatefulSet and single-replica
direct modes retain their previous behavior. An explicit boolean always wins;
deployment.yaml rejects the unsafe explicit-false combination.
*/}}
{{- define "llm-request-router.backendRouterEnabled" -}}
{{- $backendRouter := .Values.llmRequestRouter.backendRouter | default dict -}}
{{- $configured := get $backendRouter "enabled" -}}
{{- if kindIs "bool" $configured -}}
{{- $configured -}}
{{- else if and
      (eq (include "llm-request-router.workloadKind" .) "Deployment")
      (gt (.Values.llmRequestRouter.replicaCount | int) 1) -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{- define "llm-request-router.isValidDnsName" -}}
{{- $name := .name | toString | lower -}}
{{- $labels := splitList "." $name -}}
{{- $valid := and
      (gt (len $name) 0)
      (le (len $name) 253)
      (not (hasPrefix "." $name))
      (not (hasSuffix "." $name)) -}}
{{- range $label := $labels -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" $label) -}}
{{- $valid = false -}}
{{- end -}}
{{- end -}}
{{- if regexMatch "^[0-9]+$" (last $labels) -}}
{{- $valid = false -}}
{{- end -}}
{{- if $valid -}}true{{- end -}}
{{- end -}}

{{/*
The Certificate that gets issued is the configured dnsNames plus, when backend
routing is on, the wildcard form of the advertised pod hostname. Validation and
rendering must agree on that list, so both read it from here.
*/}}
{{- define "llm-request-router.effectiveCertificateDnsNames" -}}
{{- $certificate := .Values.llmRequestRouter.certificate | default dict -}}
{{- $dnsNames := dig "dnsNames" (list) $certificate -}}
{{- if eq (include "llm-request-router.backendRouterEnabled" .) "true" -}}
{{- $wildcard := replace "{pod_name}" "*" (include "llm-request-router.advertisedHostnameTemplate" .) -}}
{{- if not (has $wildcard $dnsNames) -}}
{{- $dnsNames = append $dnsNames $wildcard -}}
{{- end -}}
{{- end -}}
{{- toJson $dnsNames -}}
{{- end -}}

{{/*
Certificate wildcards follow rustls-webpki rules: only a complete leftmost
label may be a wildcard, and it matches exactly one hostname label. Replace
the runtime placeholder with a representative StatefulSet pod name before
comparing suffixes.
*/}}
{{- define "llm-request-router.validateCertificateDnsNames" -}}
{{- $certificate := .Values.llmRequestRouter.certificate | default dict -}}
{{- if $certificate.enabled -}}
{{- $dnsNames := include "llm-request-router.effectiveCertificateDnsNames" . | fromJsonArray -}}
{{- if eq (len $dnsNames) 0 -}}
{{- fail "llmRequestRouter.certificate.dnsNames is required when certificate.enabled is true" -}}
{{- end -}}
{{- $advertisedHostnameTemplate := include "llm-request-router.advertisedHostnameTemplate" . -}}
{{- $resolvedTemplate := replace "{namespace}" (include "llm-request-router.namespace" .) $advertisedHostnameTemplate -}}
{{- $hasPodName := contains "{pod_name}" $resolvedTemplate -}}
{{- $templateLabels := splitList "." $resolvedTemplate -}}
{{- $podNameOutsideLeftmostLabel := false -}}
{{- range $index, $label := $templateLabels -}}
{{- if and (gt $index 0) (contains "{pod_name}" $label) -}}
{{- $podNameOutsideLeftmostLabel = true -}}
{{- end -}}
{{- end -}}
{{- $podNameInLeftmostLabel := and $hasPodName (contains "{pod_name}" (first $templateLabels)) -}}
{{- $samplePodName := printf "%s-0" (include "llm-request-router.fullname" .) -}}
{{- $rawHostname := replace "{pod_name}" $samplePodName $resolvedTemplate | lower -}}
{{- $hostname := trimSuffix "." $rawHostname -}}
{{- $hasUnsupportedPlaceholder := regexMatch "\\{[^{}]+\\}" $hostname -}}
{{- $validHostname := and
      (le (len $rawHostname) 253)
      (eq (include "llm-request-router.isValidDnsName" (dict "name" $hostname)) "true") -}}
{{- $hostnameLabels := splitList "." $hostname -}}
{{- $covered := false -}}
{{- if and (not $hasUnsupportedPlaceholder) $validHostname -}}
{{- range $configuredDnsName := $dnsNames -}}
{{- $dnsName := $configuredDnsName | toString | lower -}}
{{- $validExactDnsName := eq (include "llm-request-router.isValidDnsName" (dict "name" $dnsName)) "true" -}}
{{- if and (not $hasPodName) $validExactDnsName (eq $dnsName $hostname) -}}
{{- $covered = true -}}
{{- else if hasPrefix "*." $dnsName -}}
{{- $wildcardSuffix := trimPrefix "*." $dnsName -}}
{{- $wildcardSuffixLabels := splitList "." $wildcardSuffix -}}
{{- $validWildcard := and
      (le (len $dnsName) 253)
      (ge (len $wildcardSuffixLabels) 2)
      (eq (include "llm-request-router.isValidDnsName" (dict "name" $wildcardSuffix)) "true") -}}
{{- $podTemplateCanUseWildcard := or
      (not $hasPodName)
      (and $podNameInLeftmostLabel (not $podNameOutsideLeftmostLabel)) -}}
{{- if and
      $validWildcard
      $podTemplateCanUseWildcard
      (eq (len $hostnameLabels) (add1 (len $wildcardSuffixLabels)))
      (ne (first $hostnameLabels) "")
      (hasSuffix (printf ".%s" $wildcardSuffix) $hostname) -}}
{{- $covered = true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if not $covered -}}
{{- fail (printf "advertised hostname template %q is not covered by llmRequestRouter.certificate.dnsNames %s" $advertisedHostnameTemplate (toJson $dnsNames)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The OpenBao signing role this chart provisions is created with
allow_subdomains=true, allow_bare_domains=false, and allow_wildcard_certificates=true.
A SAN outside allowed_domains renders cleanly and then fails at issuance, so
check it here instead. Only applies when this chart owns both the Certificate
and the role.

Coverage, matching the role flags:
  name.sub.domain   covered when it is a strict subdomain of an allowed domain
  *.sub.domain      same, and additionally when the wildcard sits directly on an
                    allowed domain
  domain            never covered on its own, because bare issuance is refused
*/}}
{{- define "llm-request-router.validatePkiAllowedDomains" -}}
{{- $pki := .Values.llmRequestRouter.pki | default dict -}}
{{- $certificate := .Values.llmRequestRouter.certificate | default dict -}}
{{- if and $pki.enabled $certificate.enabled -}}
{{- $configured := $pki.allowedDomains | default "" | toString -}}
{{- $allowed := splitList "," $configured -}}
{{- $dnsNames := include "llm-request-router.effectiveCertificateDnsNames" . | fromJsonArray -}}
{{- range $dnsName := $dnsNames -}}
{{- $name := $dnsName | toString | lower | trim -}}
{{- $isWildcard := hasPrefix "*." $name -}}
{{- $base := trimPrefix "*." $name -}}
{{- $covered := false -}}
{{- range $allowedDomain := $allowed -}}
{{- $domain := $allowedDomain | toString | lower | trim -}}
{{- if $domain -}}
{{- if hasSuffix (printf ".%s" $domain) $base -}}
{{- $covered = true -}}
{{- else if and $isWildcard (eq $base $domain) -}}
{{- $covered = true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if not $covered -}}
{{- fail (printf "certificate DNS name %q is not covered by llmRequestRouter.pki.allowedDomains %q. The OpenBao signing role uses allow_subdomains=true and allow_bare_domains=false, so cert-manager issuance would be rejected after a successful render. Add a covering suffix, for example cluster.local for in-cluster names." $dnsName $configured) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "llm-request-router.serviceAccountName" -}}
{{- if .Values.llmRequestRouter.serviceAccount.create }}
{{- default (include "llm-request-router.fullname" .) .Values.llmRequestRouter.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.llmRequestRouter.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "llm-request-router.backendRouterServiceAccountName" -}}
{{- $serviceAccount := .Values.llmRequestRouter.backendRouter.serviceAccount | default dict -}}
{{- if $serviceAccount.create -}}
{{- default (include "llm-request-router.backendRouterName" .) $serviceAccount.name -}}
{{- else -}}
{{- required "llmRequestRouter.backendRouter.serviceAccount.name is required when backendRouter is enabled and backendRouter.serviceAccount.create is false" $serviceAccount.name -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.image" -}}
{{- $registry := .Values.llmRequestRouter.image.registry -}}
{{- $repository := .Values.llmRequestRouter.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.llmRequestRouter.image.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.advertisedHostnameTemplate" -}}
{{- $configured := .Values.llmRequestRouter.kubernetes.advertisedHostnameTemplate -}}
{{- $backendRouterEnabled := eq (include "llm-request-router.backendRouterEnabled" .) "true" -}}
{{- if and $backendRouterEnabled $configured (ne (len (splitList "{pod_name}" $configured)) 2) -}}
{{- fail "llmRequestRouter.kubernetes.advertisedHostnameTemplate must contain exactly one {pod_name} when backendRouter.enabled is true" -}}
{{- end -}}
{{- if $configured -}}
{{- $configured -}}
{{- else if or $backendRouterEnabled (gt (.Values.llmRequestRouter.replicaCount | int) 1) -}}
{{- printf "{pod_name}.%s.%s.svc.cluster.local" .Values.llmRequestRouter.service.headlessName (include "llm-request-router.namespace" .) -}}
{{- else -}}
{{- printf "%s.%s.svc.cluster.local" (include "llm-request-router.fullname" .) (include "llm-request-router.namespace" .) -}}
{{- end -}}
{{- end }}

{{/*
Workers dial these to reach the router. In-cluster the backend-router Service is
the right answer and needs no configuration, which is what lets backend routing
be on by default. Split-cluster and multi-region operators override both with an
externally reachable address.
*/}}
{{- define "llm-request-router.backendRouterGrpcDialAddress" -}}
{{- $backendRouter := .Values.llmRequestRouter.backendRouter | default dict -}}
{{- $configured := dig "pylonGrpcDialAddress" "" $backendRouter | toString | trim -}}
{{- if $configured -}}
{{- if ne (include "llm-request-router.isExplicitHttpUri" $configured) "true" -}}
{{- fail "llmRequestRouter.backendRouter.pylonGrpcDialAddress must be an explicit http:// or https:// URI" -}}
{{- end -}}
{{- $configured -}}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:%v" (include "llm-request-router.backendRouterName" .) (include "llm-request-router.namespace" .) (dig "service" "grpcPort" 50071 $backendRouter) -}}
{{- end -}}
{{- end -}}

{{- define "llm-request-router.backendRouterReverseTunnelDialAddress" -}}
{{- $backendRouter := .Values.llmRequestRouter.backendRouter | default dict -}}
{{- $configured := dig "pylonReverseTunnelDialAddress" "" $backendRouter | toString | trim -}}
{{- if $configured -}}
{{- $configured -}}
{{- else -}}
{{- printf "%s.%s.svc.cluster.local:%v" (include "llm-request-router.backendRouterName" .) (include "llm-request-router.namespace" .) (dig "service" "reverseTunnelPort" 50072 $backendRouter) -}}
{{- end -}}
{{- end -}}

{{- define "llm-request-router.backendRouterImage" -}}
{{- $image := .Values.llmRequestRouter.backendRouter.image -}}
{{- $registry := default .Values.llmRequestRouter.image.registry $image.registry -}}
{{- $repository := required "llmRequestRouter.backendRouter.image.repository or llmRequestRouter.image.repository is required when backend routing is enabled" (default .Values.llmRequestRouter.image.repository $image.repository) -}}
{{- $tag := include "llm-request-router.backendRouterImageTag" . -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.pkiMigrationsImage" -}}
{{- $img := .Values.llmRequestRouter.pki.image -}}
{{- $registry := $img.registry -}}
{{- $repository := required "llmRequestRouter.pki.image.repository is required when llmRequestRouter.pki.enabled is true" $img.repository -}}
{{- $tag := required "llmRequestRouter.pki.image.tag is required when llmRequestRouter.pki.enabled is true" $img.tag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end }}

{{/*
Validate the QUIC server identity source. certManager keeps cert-manager as the
owner of issuance and renewal. existingSecret mounts a pre-created TLS Secret,
renders no Certificate, and makes the operator the owner. The two are mutually
exclusive, and existingSecret needs the Secret name plus both file paths because
the mount and the Stargate arguments are all conditional on them.
*/}}
{{- define "llm-request-router.validateTlsIdentity" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- $certificate := .Values.llmRequestRouter.certificate | default dict -}}
{{- $mode := $tls.mode | default "certManager" -}}
{{- if not (has $mode (list "certManager" "existingSecret")) -}}
{{- fail (printf "llmRequestRouter.tls.mode must be certManager or existingSecret, got %q" (toString $mode)) -}}
{{- end -}}
{{- if eq $mode "existingSecret" -}}
{{- if $certificate.enabled -}}
{{- fail "llmRequestRouter.certificate.enabled must be false when llmRequestRouter.tls.mode is existingSecret; cert-manager and the operator cannot both own the request-router certificate" -}}
{{- end -}}
{{- if not $tls.secretName -}}
{{- fail "llmRequestRouter.tls.secretName is required when llmRequestRouter.tls.mode is existingSecret" -}}
{{- end -}}
{{- if not $tls.certPath -}}
{{- fail "llmRequestRouter.tls.certPath is required when llmRequestRouter.tls.mode is existingSecret" -}}
{{- end -}}
{{- if not $tls.keyPath -}}
{{- fail "llmRequestRouter.tls.keyPath is required when llmRequestRouter.tls.mode is existingSecret" -}}
{{- end -}}
{{- $tlsMountPath := include "llm-request-router.tlsMountPath" . | trim -}}
{{- if not (hasPrefix "/" $tlsMountPath) -}}
{{- fail "llmRequestRouter.tls.mountPath must be an absolute path when llmRequestRouter.tls.mode is existingSecret" -}}
{{- end -}}
{{- if or (ne $tlsMountPath (dir $tls.certPath)) (ne $tlsMountPath (dir $tls.keyPath)) -}}
{{- fail "llmRequestRouter.tls.mountPath must match the directory containing llmRequestRouter.tls.certPath and llmRequestRouter.tls.keyPath when llmRequestRouter.tls.mode is existingSecret" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.tlsSecretName" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- $certificate := .Values.llmRequestRouter.certificate | default dict -}}
{{- if $tls.secretName -}}
{{- $tls.secretName -}}
{{- else if $certificate.secretName -}}
{{- $certificate.secretName -}}
{{- else if $certificate.enabled -}}
{{- printf "%s-quic-tls" (include "llm-request-router.fullname" .) -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.tlsMountPath" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- if $tls.mountPath -}}
{{- $tls.mountPath -}}
{{- else if $tls.certPath -}}
{{- dir $tls.certPath -}}
{{- else -}}
/etc/stargate/tls
{{- end -}}
{{- end }}

{{- define "llm-request-router.validateTlsCertKeyDir" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- if and $tls.certPath $tls.keyPath (ne (dir $tls.certPath) (dir $tls.keyPath)) -}}
{{- fail "llmRequestRouter.tls.certPath and llmRequestRouter.tls.keyPath must use the same directory" -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.validateBackendRouterTls" -}}
{{- $tls := .Values.llmRequestRouter.tls | default dict -}}
{{- $secretName := include "llm-request-router.tlsSecretName" . -}}
{{- $hasSecret := not (empty $secretName) -}}
{{- $hasCert := not (empty $tls.certPath) -}}
{{- $hasKey := not (empty $tls.keyPath) -}}
{{- $hasAny := or $hasSecret $hasCert $hasKey -}}
{{- $hasAll := and $hasSecret $hasCert $hasKey -}}
{{- if and $hasAny (not $hasAll) -}}
{{- fail "llmRequestRouter backend routing requires tls.secretName (or certificate secret), tls.certPath, and tls.keyPath together" -}}
{{- end -}}
{{- if and (not $tls.quicInsecure) (not $hasAll) -}}
{{- fail "llmRequestRouter backend routing requires a TLS Secret and cert/key paths when tls.quicInsecure is false" -}}
{{- end -}}
{{- if $hasAll -}}
{{- include "llm-request-router.validateTlsCertKeyDir" . -}}
{{- end -}}
{{- if and $hasAll (ne (clean (include "llm-request-router.tlsMountPath" .)) (clean (dir $tls.certPath))) -}}
{{- fail "llmRequestRouter.tls.mountPath must match the directory containing tls.certPath and tls.keyPath" -}}
{{- end -}}
{{- end }}

{{- define "llm-request-router.validateBackendRouterServiceAccount" -}}
{{- $_ := include "llm-request-router.backendRouterServiceAccountName" . -}}
{{- end }}

{{/*
Vault Annotations
*/}}
{{- define "llm-request-router.vaultAnnotations" -}}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ if .Values.llmRequestRouter.vault }}{{ .Values.llmRequestRouter.vault.vaultRole | default "llm-request-router" }}{{ else }}"llm-request-router"{{ end }}
vault.hashicorp.com/auth-path: "auth/jwt"
vault.hashicorp.com/agent-copy-volume-mounts: {{ .Chart.Name }}
vault.hashicorp.com/agent-run-as-same-user: "true"
{{- if .Values.llmRequestRouter.vault }}
{{- if .Values.llmRequestRouter.vault.jwtAuthPath }}
vault.hashicorp.com/jwt-auth-path: {{ .Values.llmRequestRouter.vault.jwtAuthPath }}
{{- end }}
{{- if .Values.llmRequestRouter.vault.vaultAddress }}
vault.hashicorp.com/service: {{ .Values.llmRequestRouter.vault.vaultAddress }}
{{- end }}
{{- if .Values.llmRequestRouter.vault.vaultNamespace }}
vault.hashicorp.com/namespace: {{ .Values.llmRequestRouter.vault.vaultNamespace }}
{{- end }}
{{- end }}
vault.hashicorp.com/agent-service-account-token-volume-name: vault-token
vault.hashicorp.com/agent-inject-template-file-secrets.json: "/vault/config/templates/secrets.json.tmpl"
vault.hashicorp.com/secret-volume-path: "/vault/secrets"
{{- end }}

{{/*
Generate all pod annotations
*/}}
{{- define "llm-request-router.podAnnotations" -}}
{{- $annotations := dict -}}

{{- if .Values.llmRequestRouter.podAnnotations -}}
{{- $annotations = merge $annotations .Values.llmRequestRouter.podAnnotations -}}
{{- end -}}

{{- if not (and .Values.llmRequestRouter.vault .Values.llmRequestRouter.vault.noVaultAnnotations) -}}
{{- $vaultAnnotations := include "llm-request-router.vaultAnnotations" . | fromYaml -}}
{{- if $vaultAnnotations -}}
{{- $annotations = merge $annotations $vaultAnnotations -}}
{{- end -}}
{{- end -}}

{{- toYaml $annotations -}}
{{- end }}
