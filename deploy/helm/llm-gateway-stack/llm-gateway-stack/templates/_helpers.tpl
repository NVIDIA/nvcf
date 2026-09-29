{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{- define "llm-gateway-stack.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: llm-gateway-stack
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Subchart values. The stack reads names from them so that the Secrets it renders
are the ones the subcharts mount.
*/}}
{{- define "llm-gateway-stack.routerValues" -}}
{{- toJson (index .Values "llm-request-router" "llmRequestRouter") -}}
{{- end }}

{{- define "llm-gateway-stack.gatewayValues" -}}
{{- toJson (index .Values "llm-api-gateway" "llmApiGateway") -}}
{{- end }}

{{/*
Service names, resolved the way the subcharts resolve their fullname.
*/}}
{{- define "llm-gateway-stack.routerName" -}}
{{- $r := include "llm-gateway-stack.routerValues" . | fromJson -}}
{{- $r.fullnameOverride | default $r.nameOverride | default "llm-request-router" | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "llm-gateway-stack.gatewayName" -}}
{{- $g := include "llm-gateway-stack.gatewayValues" . | fromJson -}}
{{- $g.fullnameOverride | default $g.nameOverride | default "llm-api-gateway" | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Whether the router runs the backend router: an explicit boolean wins, otherwise
a multi-replica Deployment turns it on, as in the router chart.
*/}}
{{- define "llm-gateway-stack.routerBackendRouterEnabled" -}}
{{- $r := include "llm-gateway-stack.routerValues" . | fromJson -}}
{{- $configured := dig "backendRouter" "enabled" nil $r -}}
{{- if kindIs "bool" $configured -}}
{{- if $configured }}true{{ end -}}
{{- else if and (eq (dig "workload" "kind" "Deployment" $r) "Deployment") (gt (int (dig "replicaCount" 1 $r)) 1) -}}
true
{{- end -}}
{{- end }}

{{/*
Worker gRPC address for the pylon-operator chart's router.grpcAddress.
*/}}
{{- define "llm-gateway-stack.routerGrpcAddress" -}}
{{- $r := include "llm-gateway-stack.routerValues" . | fromJson -}}
{{- $name := include "llm-gateway-stack.routerName" . -}}
{{- if include "llm-gateway-stack.routerBackendRouterEnabled" . -}}
{{- printf "%s-backend-router.%s.svc.cluster.local:%v" $name .Release.Namespace (dig "backendRouter" "service" "grpcPort" 50071 $r) -}}
{{- else -}}
{{- printf "%s.%s.svc.cluster.local:%v" $name .Release.Namespace (dig "service" "grpcPort" 50071 $r) -}}
{{- end -}}
{{- end }}

{{- define "llm-gateway-stack.serviceDnsNames" -}}
{{- $names := list .name (printf "%s.%s" .name .namespace) (printf "%s.%s.svc" .name .namespace) (printf "%s.%s.svc.cluster.local" .name .namespace) -}}
{{- toJson $names -}}
{{- end }}

{{/*
DNS names on the router certificate. Pylon verifies the QUIC server name it
dials: the router Service for one replica, the per-pod headless name for
several, and the backend router Service when that fronts the pods.
*/}}
{{- define "llm-gateway-stack.routerDnsNames" -}}
{{- $r := include "llm-gateway-stack.routerValues" . | fromJson -}}
{{- $ns := .Release.Namespace -}}
{{- $name := include "llm-gateway-stack.routerName" . -}}
{{- $names := include "llm-gateway-stack.serviceDnsNames" (dict "name" $name "namespace" $ns) | fromJsonArray -}}
{{- $names = concat $names (include "llm-gateway-stack.serviceDnsNames" (dict "name" (printf "%s-backend-router" $name | trunc 63 | trimSuffix "-") "namespace" $ns) | fromJsonArray) -}}
{{- $headless := dig "service" "headlessName" (printf "%s-headless" $name) $r -}}
{{- $names = append $names (printf "*.%s.%s.svc.cluster.local" $headless $ns) -}}
{{- with dig "kubernetes" "advertisedHostnameTemplate" "" $r -}}
{{- $names = append $names (. | replace "{namespace}" $ns | replace "{pod_name}" "*") -}}
{{- end -}}
{{- $names = concat $names (.Values.tls.selfSigned.routerDnsNames | default (list)) -}}
{{- toJson ($names | uniq) -}}
{{- end }}

{{- define "llm-gateway-stack.gatewayDnsNames" -}}
{{- $names := include "llm-gateway-stack.serviceDnsNames" (dict "name" (include "llm-gateway-stack.gatewayName" .) "namespace" .Release.Namespace) | fromJsonArray -}}
{{- $names = concat $names (.Values.tls.selfSigned.gatewayDnsNames | default (list)) -}}
{{- toJson ($names | uniq) -}}
{{- end }}

{{/*
Router credential hashes as a JSON list of sha256:<lowercase hex>:
clusterCredential.sha256 first, then clusterCredential.sha256Hashes.
llm-gateway-stack.validate checks the entries.
*/}}
{{- define "llm-gateway-stack.clusterCredentialHashes" -}}
{{- $hashes := list -}}
{{- range (include "llm-gateway-stack.clusterCredentialEntries" . | fromJsonArray) -}}
{{- $hashes = append $hashes (printf "sha256:%s" (. | toString | trimPrefix "sha256:" | lower)) -}}
{{- end -}}
{{- toJson $hashes -}}
{{- end }}

{{/*
Raw clusterCredential.sha256 and clusterCredential.sha256Hashes entries as a
JSON list, in rendering order.
*/}}
{{- define "llm-gateway-stack.clusterCredentialEntries" -}}
{{- $entries := list -}}
{{- with .Values.clusterCredential.sha256 -}}
{{- $entries = append $entries (. | toString) -}}
{{- end -}}
{{- $more := .Values.clusterCredential.sha256Hashes | default list -}}
{{- if not (kindIs "slice" $more) -}}
{{- fail "llm-gateway-stack: clusterCredential.sha256Hashes must be a list" -}}
{{- end -}}
{{- range $more -}}
{{- $entries = append $entries (. | toString) -}}
{{- end -}}
{{- toJson $entries -}}
{{- end }}

{{/*
Validate the stack values. Every missing required value is reported at once.
Renders nothing.
*/}}
{{- define "llm-gateway-stack.validate" -}}
{{- $r := include "llm-gateway-stack.routerValues" . | fromJson -}}
{{- $g := include "llm-gateway-stack.gatewayValues" . | fromJson -}}
{{- $sha256 := "^[0-9a-f]{64}$" -}}
{{- $missing := list -}}
{{- if not (dig "image" "repository" "" $r) -}}
{{- $missing = append $missing "llm-request-router.llmRequestRouter.image.repository" -}}
{{- end -}}
{{- if not (dig "image" "repository" "" $g) -}}
{{- $missing = append $missing "llm-api-gateway.llmApiGateway.image.repository" -}}
{{- end -}}
{{- if .Values.clusterCredential.create -}}
{{- if not .Values.clusterId -}}
{{- $missing = append $missing "clusterId" -}}
{{- end -}}
{{- if not (include "llm-gateway-stack.clusterCredentialEntries" . | fromJsonArray) -}}
{{- $missing = append $missing "clusterCredential.sha256 or clusterCredential.sha256Hashes" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.apiKeysSecret.create (not .Values.apiKeys) -}}
{{- $missing = append $missing "apiKeys" -}}
{{- end -}}
{{- if $missing -}}
{{- fail (printf "llm-gateway-stack: set the required values: %s" (join ", " $missing)) -}}
{{- end -}}

{{- range $chart, $ns := dict "llm-request-router.llmRequestRouter" (dig "namespace" "" $r) "llm-api-gateway.llmApiGateway" (dig "namespace" "" $g) -}}
{{- if and $ns (ne $ns $.Release.Namespace) -}}
{{- fail (printf "llm-gateway-stack: %s.namespace must be empty or the release namespace %q; the stack runs in one namespace" $chart $.Release.Namespace) -}}
{{- end -}}
{{- end -}}

{{- if .Values.clusterCredential.create -}}
{{- if or (gt (len .Values.clusterId) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" .Values.clusterId)) -}}
{{- fail (printf "llm-gateway-stack: clusterId %q must be a DNS label: lowercase letters, digits and '-', at most 63 characters" .Values.clusterId) -}}
{{- end -}}
{{- $hashes := include "llm-gateway-stack.clusterCredentialHashes" . | fromJsonArray -}}
{{- $offset := ternary 1 0 (not (empty .Values.clusterCredential.sha256)) -}}
{{- range $i, $entry := include "llm-gateway-stack.clusterCredentialEntries" . | fromJsonArray -}}
{{- $name := ternary "clusterCredential.sha256" (printf "clusterCredential.sha256Hashes[%d]" (sub $i $offset)) (lt $i $offset) -}}
{{- if not (regexMatch "^(sha256:)?[0-9A-Fa-f]{64}$" $entry) -}}
{{- fail (printf "llm-gateway-stack: %s must be a 64 character hex SHA-256 digest of the cluster token, optionally prefixed with sha256:" $name) -}}
{{- end -}}
{{- if has (index $hashes $i) (slice $hashes 0 $i) -}}
{{- fail (printf "llm-gateway-stack: %s repeats another cluster credential hash" $name) -}}
{{- end -}}
{{- end -}}
{{- if or (dig "auth" "workerAuthEndpoint" "" $r) (not (dig "auth" "credentialsSecret" "name" "" $r)) -}}
{{- fail "llm-gateway-stack: clusterCredential.create needs the router in static credentials mode: set llm-request-router.llmRequestRouter.auth.workerAuthEndpoint to \"\" and auth.credentialsSecret.name" -}}
{{- end -}}
{{- end -}}

{{- if .Values.apiKeysSecret.create -}}
{{- if or (ne (dig "auth" "mode" "" $g) "staticKeys") (not (dig "auth" "staticKeys" "existingSecret" "" $g)) -}}
{{- fail "llm-gateway-stack: apiKeysSecret.create needs the gateway in static key mode: set llm-api-gateway.llmApiGateway.auth.mode to staticKeys and auth.staticKeys.existingSecret" -}}
{{- end -}}
{{- $ids := list -}}
{{- $digests := list -}}
{{- range $i, $key := .Values.apiKeys -}}
{{- $id := $key.id | default "" | toString -}}
{{- $digest := $key.sha256 | default "" | toString | lower -}}
{{- if not (regexMatch "^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$" $id) -}}
{{- fail (printf "llm-gateway-stack: apiKeys[%d].id %q must be 1 to 128 letters, digits, '.', '_' or '-', starting with a letter or digit" $i $id) -}}
{{- end -}}
{{- if not (regexMatch $sha256 $digest) -}}
{{- fail (printf "llm-gateway-stack: apiKeys[%d].sha256 must be a 64 character hex SHA-256 digest of the key" $i) -}}
{{- end -}}
{{- if has $id $ids -}}
{{- fail (printf "llm-gateway-stack: apiKeys id %q is listed twice" $id) -}}
{{- end -}}
{{- if has $digest $digests -}}
{{- fail (printf "llm-gateway-stack: apiKeys[%d].sha256 repeats another key's digest" $i) -}}
{{- end -}}
{{- $ids = append $ids $id -}}
{{- $digests = append $digests $digest -}}
{{- end -}}
{{- end -}}

{{- if .Values.tls.selfSigned.enabled -}}
{{- if or (ne (dig "tls" "mode" "" $r) "existingSecret") (not (dig "tls" "secretName" "" $r)) -}}
{{- fail "llm-gateway-stack: tls.selfSigned needs llm-request-router.llmRequestRouter.tls.mode existingSecret and tls.secretName" -}}
{{- end -}}
{{- if not (and (dig "tls" "enabled" false $g) (dig "tls" "existingSecret" "" $g)) -}}
{{- fail "llm-gateway-stack: tls.selfSigned needs llm-api-gateway.llmApiGateway.tls.enabled and tls.existingSecret" -}}
{{- end -}}
{{- if lt (int .Values.tls.selfSigned.validityDays) 1 -}}
{{- fail "llm-gateway-stack: tls.selfSigned.validityDays must be at least 1" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Server certificate for one Secret, as JSON {"crt", "key"} with base64 values.
The installed certificate is reused when the CA was reused and the Secret
records the same names; otherwise a new one is signed.
Arguments: dict "root" . "ca" <ca> "caReused" <bool> "secretName" <name>
"commonName" <cn> "dnsNames" <list> "ips" <list> "names" <comma-joined names>.
*/}}
{{- define "llm-gateway-stack.serverCert" -}}
{{- $root := .root -}}
{{- $existing := lookup "v1" "Secret" $root.Release.Namespace .secretName -}}
{{- $reuse := false -}}
{{- if and .caReused $existing $existing.data (index $existing.data "tls.crt") (index $existing.data "tls.key") -}}
{{- $annotations := $existing.metadata.annotations | default dict -}}
{{- if eq (index $annotations "llm-gateway-stack.nvidia.com/subject-alt-names" | default "") .names -}}
{{- $reuse = true -}}
{{- end -}}
{{- end -}}
{{- if $reuse -}}
{{- toJson (dict "crt" (index $existing.data "tls.crt") "key" (index $existing.data "tls.key")) -}}
{{- else -}}
{{- $cert := genSignedCert .commonName .ips .dnsNames (int $root.Values.tls.selfSigned.validityDays) .ca -}}
{{- toJson (dict "crt" ($cert.Cert | b64enc) "key" ($cert.Key | b64enc)) -}}
{{- end -}}
{{- end }}
