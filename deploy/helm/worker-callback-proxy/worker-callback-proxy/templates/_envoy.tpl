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

{{- define "workerCallbackProxy.dnsCache" -}}
dns_cache_config:
  name: dfp_cache
  dns_lookup_family: V4_PREFERRED
  typed_dns_resolver_config:
    name: envoy.network.dns_resolver.getaddrinfo
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.network.dns_resolver.getaddrinfo.v3.GetAddrInfoDnsResolverConfig
{{- end -}}

{{- define "workerCallbackProxy.accessLog" -}}
access_log:
  - name: envoy.access_loggers.stdout
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.access_loggers.stream.v3.StdoutAccessLog
      log_format:
        json_format:
          time: "%START_TIME%"
          log_type: access
          listener: {{ . | quote }}
          x_request_id: "%REQ(X-REQUEST-ID)%"
          trace_id: "%TRACE_ID%"
          connection_id: "%CONNECTION_ID%"
          method: "%REQ(:METHOD)%"
          authority: "%REQ(:AUTHORITY)%"
          protocol: "%PROTOCOL%"
          response_code: "%RESPONSE_CODE%"
          response_flags: "%RESPONSE_FLAGS%"
          response_code_details: "%RESPONSE_CODE_DETAILS%"
          upstream_host: "%UPSTREAM_HOST%"
          upstream_transport_failure_reason: "%UPSTREAM_TRANSPORT_FAILURE_REASON%"
          duration_ms: "%DURATION%"
          bytes_received: "%BYTES_RECEIVED%"
          bytes_sent: "%BYTES_SENT%"
          downstream_remote_address: "%DOWNSTREAM_REMOTE_ADDRESS%"
{{- end -}}

{{- define "workerCallbackProxy.tracing" -}}
{{- $tr := .Values.workerCallbackProxy.tracing -}}
{{- if $tr.endpoint -}}
tracing:
  random_sampling: { value: {{ $tr.samplingPercentage }} }
  provider:
    name: envoy.tracers.opentelemetry
    typed_config:
      "@type": type.googleapis.com/envoy.config.trace.v3.OpenTelemetryConfig
      service_name: {{ $tr.serviceName | quote }}
      grpc_service:
        envoy_grpc: { cluster_name: opentelemetry_collector }
        timeout: 1s
{{- end -}}
{{- end -}}

{{- define "workerCallbackProxy.circuitBreakers" -}}
circuit_breakers:
  thresholds:
    - priority: DEFAULT
      max_connections: {{ int .maxUpstreamConnections }}
      max_pending_requests: {{ int .maxPendingRequests }}
      max_requests: {{ int .maxRequests }}
{{- end -}}

{{/*
A listener that forwards one admin path, so the admin API itself stays on
127.0.0.1.
*/}}
{{- define "workerCallbackProxy.adminPathListener" -}}
- name: {{ .name }}
  address:
    socket_address: { protocol: TCP, address: 0.0.0.0, port_value: {{ int .port }} }
  filter_chains:
    - filters:
        - name: envoy.filters.network.http_connection_manager
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
            stat_prefix: {{ .name }}
            route_config:
              name: {{ .name }}
              virtual_hosts:
                - name: {{ .name }}
                  domains: ["*"]
                  routes:
                    - match: { path: {{ .path | quote }} }
                      route: { cluster: admin }
                    - match: { prefix: "/" }
                      direct_response: { status: 404 }
            http_filters:
              - name: envoy.filters.http.router
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
{{- end -}}

{{- define "workerCallbackProxy.envoyConfig" -}}
{{- $c := .Values.workerCallbackProxy -}}
{{- $limits := $c.connectionLimits -}}
{{- $t := $c.timeouts -}}
node:
  id: {{ include "workerCallbackProxy.fullname" . }}
  cluster: {{ include "workerCallbackProxy.fullname" . }}
admin:
  address:
    socket_address: { protocol: TCP, address: 127.0.0.1, port_value: {{ int $c.ports.admin }} }
overload_manager:
  refresh_interval: 0.25s
  resource_monitors:
    - name: envoy.resource_monitors.global_downstream_max_connections
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig
        max_active_downstream_connections: {{ int $limits.maxActiveDownstreamConnections }}
static_resources:
  listeners:
    # Kubelet probes.
    {{- include "workerCallbackProxy.adminPathListener" (dict "name" "health" "port" $c.ports.health "path" "/ready") | nindent 4 }}
    # Prometheus scrape, through the metrics Service.
    {{- include "workerCallbackProxy.adminPathListener" (dict "name" "metrics" "port" $c.ports.metrics "path" "/stats/prometheus") | nindent 4 }}
  {{- if $c.grpcProxy.enabled }}
    # grpc-proxy worker tunnels: HTTP/3 from the worker. TLS ends here and is
    # re-established to the grpc-proxy pod named by the callback hostname.
    - name: grpc_worker_quic
      address:
        socket_address: { protocol: UDP, address: 0.0.0.0, port_value: {{ int $c.ports.grpcQuic }} }
      udp_listener_config:
        quic_options: { idle_timeout: {{ $t.quicIdle }} }
        downstream_socket_config: { prefer_gro: true }
      filter_chains:
        - transport_socket:
            name: envoy.transport_sockets.quic
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.transport_sockets.quic.v3.QuicDownstreamTransport
              downstream_tls_context:
                common_tls_context:
                  tls_certificate_sds_secret_configs:
                    - name: frontend_tls
                      sds_config:
                        path_config_source:
                          path: /etc/envoy/config/sds_frontend.yaml
          filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                codec_type: HTTP3
                stat_prefix: grpc_worker_quic
                {{- include "workerCallbackProxy.accessLog" "grpc_worker_quic" | nindent 16 }}
                {{- with include "workerCallbackProxy.tracing" . }}
                {{- . | nindent 16 }}
                {{- end }}
                common_http_protocol_options:
                  idle_timeout: {{ $t.grpcIdle }}
                  max_connection_duration: {{ $t.grpcMaxConnectionDuration }}
                http3_protocol_options:
                  quic_protocol_options: { max_concurrent_streams: 4294967295 }
                route_config:
                  name: grpc_worker
                  virtual_hosts:
                    # The Lua filter only rewrites <dashed-ip>.<workerDomain>,
                    # and only grpc-proxy pods can complete the upstream TLS
                    # handshake.
                    - name: grpc_worker
                      domains: ["*"]
                      routes:
                        - match: { prefix: "/" }
                          route:
                            cluster: grpc_proxy_pods
                            timeout: 0s
                http_filters:
                  - name: envoy.filters.http.lua
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua
                      default_source_code:
                        inline_string: |
                          function envoy_on_request(h)
                            local host = string.lower(h:headers():get(":authority") or "")
                            host = (host:gsub(":%d+$", ""))
                            local a, b, c, d, domain = host:match("^(%d+)%-(%d+)%-(%d+)%-(%d+)%.(.+)$")
                            if a == nil or domain ~= "{{ include "workerCallbackProxy.workerDomain" . }}" then
                              h:respond({[":status"] = "404"}, "unknown callback host")
                              return
                            end
                            h:headers():replace(":authority", a .. "." .. b .. "." .. c .. "." .. d .. ":{{ int $c.grpcProxy.port }}")
                          end
                  - name: envoy.filters.http.dynamic_forward_proxy
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig
                      {{- include "workerCallbackProxy.dnsCache" . | nindent 22 }}
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  {{- end }}
  {{- if $c.invocation.enabled }}
    # invocation-service worker tunnels: HTTP/1 CONNECT and forward-proxy
    # requests, only to invocation-service pod addresses.
    - name: invocation_worker_tcp
      address:
        socket_address: { protocol: TCP, address: 0.0.0.0, port_value: {{ int $c.ports.invocationTcp }} }
      per_connection_buffer_limit_bytes: {{ int $limits.perConnectionBufferLimitBytes }}
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: invocation_worker_tcp
                {{- include "workerCallbackProxy.accessLog" "invocation_worker_tcp" | nindent 16 }}
                {{- with include "workerCallbackProxy.tracing" . }}
                {{- . | nindent 16 }}
                {{- end }}
                common_http_protocol_options:
                  idle_timeout: {{ $t.invocationIdle }}
                  max_connection_duration: {{ $t.invocationMaxConnectionDuration }}
                http_protocol_options: {}
                http2_protocol_options: { allow_connect: true }
                upgrade_configs:
                  - upgrade_type: CONNECT
                route_config:
                  name: invocation_worker
                  virtual_hosts:
                    - name: invocation_worker
                      domains: ["*"]
                      routes:
                        - match:
                            connect_matcher: {}
                            headers:
                              - name: ":authority"
                                string_match:
                                  safe_regex:
                                    regex: {{ include "workerCallbackProxy.invocationAuthorityRegex" . | squote }}
                          route:
                            cluster: invocation_pods
                            upgrade_configs:
                              - upgrade_type: CONNECT
                                connect_config: {}
                            timeout: 0s
                        # http:// callback targets arrive as absolute-form
                        # forward-proxy requests, not CONNECT.
                        - match:
                            prefix: "/"
                            headers:
                              - name: ":authority"
                                string_match:
                                  safe_regex:
                                    regex: {{ include "workerCallbackProxy.invocationAuthorityRegex" . | squote }}
                          route:
                            cluster: invocation_pods
                            host_rewrite_header: ":authority"
                            timeout: 0s
                http_filters:
                  - name: envoy.filters.http.dynamic_forward_proxy
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig
                      {{- include "workerCallbackProxy.dnsCache" . | nindent 22 }}
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  {{- end }}
  clusters:
    - name: admin
      connect_timeout: 1s
      type: STATIC
      load_assignment:
        cluster_name: admin
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address: { address: 127.0.0.1, port_value: {{ int $c.ports.admin }} }
  {{- with $c.tracing.endpoint }}
  {{- $parts := regexFindAll "^(.+):([0-9]+)$" . 1 }}
  {{- if not $parts }}
  {{- fail (printf "workerCallbackProxy.tracing.endpoint %q must be host:port" .) }}
  {{- end }}
    - name: opentelemetry_collector
      connect_timeout: 1s
      type: STRICT_DNS
      typed_extension_protocol_options:
        envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
          "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
          explicit_http_config:
            http2_protocol_options: {}
      load_assignment:
        cluster_name: opentelemetry_collector
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address: { address: {{ regexReplaceAll ":[0-9]+$" . "" | quote }}, port_value: {{ regexReplaceAll "^.+:" . "" | int }} }
  {{- end }}
  {{- if $c.grpcProxy.enabled }}
    - name: grpc_proxy_pods
      connect_timeout: {{ $t.connect }}
      lb_policy: CLUSTER_PROVIDED
      {{- include "workerCallbackProxy.circuitBreakers" $limits | nindent 6 }}
      cluster_type:
        name: envoy.clusters.dynamic_forward_proxy
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig
          allow_insecure_cluster_options: true
          {{- include "workerCallbackProxy.dnsCache" . | nindent 10 }}
      typed_extension_protocol_options:
        envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
          "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
          explicit_http_config:
            http3_protocol_options:
              quic_protocol_options: { max_concurrent_streams: 4294967295 }
          common_http_protocol_options: { idle_timeout: 35s }
      transport_socket:
        name: envoy.transport_sockets.quic
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.quic.v3.QuicUpstreamTransport
          upstream_tls_context:
            sni: {{ include "workerCallbackProxy.grpcProxyServerName" . | quote }}
            common_tls_context:
              combined_validation_context:
                default_validation_context:
                  match_typed_subject_alt_names:
                    - san_type: DNS
                      matcher: { exact: {{ include "workerCallbackProxy.grpcProxyServerName" . | quote }} }
                # The CA comes from a filesystem SDS resource that watches the
                # mounted Secret, so a rotated CA applies without a restart.
                validation_context_sds_secret_config:
                  name: grpc_proxy_ca
                  sds_config:
                    path_config_source:
                      path: /etc/envoy/config/sds_grpc_proxy_ca.yaml
  {{- end }}
  {{- if $c.invocation.enabled }}
    - name: invocation_pods
      connect_timeout: {{ $t.connect }}
      lb_policy: CLUSTER_PROVIDED
      per_connection_buffer_limit_bytes: {{ int $limits.perConnectionBufferLimitBytes }}
      {{- include "workerCallbackProxy.circuitBreakers" $limits | nindent 6 }}
      cluster_type:
        name: envoy.clusters.dynamic_forward_proxy
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig
          allow_insecure_cluster_options: true
          {{- include "workerCallbackProxy.dnsCache" . | nindent 10 }}
      typed_extension_protocol_options:
        envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
          "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
          explicit_http_config:
            http_protocol_options: {}
          common_http_protocol_options: { idle_timeout: {{ $t.invocationIdle }} }
  {{- end }}
{{- end -}}
