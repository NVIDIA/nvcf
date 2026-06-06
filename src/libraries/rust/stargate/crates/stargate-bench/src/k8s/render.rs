// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

use crate::config::{AlgorithmConfig, BenchmarkConfig};
use crate::runtime::{BackendRuntimeSpec, PylonRuntimeSpec};

use super::images::ImageRefs;

pub(super) fn render_stargate_external_services(namespace: &str, pods: &[StargatePod]) -> String {
    let mut manifest = String::new();
    for pod in pods {
        let service_name = format!("{}-external", pod.name);
        manifest.push_str(&format!(
            "apiVersion: v1\nkind: Service\nmetadata:\n  name: {service_name}\n  namespace: {namespace}\nspec:\n  selector:\n    statefulset.kubernetes.io/pod-name: {pod_name}\n  ports:\n    - name: grpc\n      port: 50071\n      targetPort: grpc\n    - name: http\n      port: 8000\n      targetPort: http\n    - name: reverse\n      port: 50072\n      targetPort: reverse\n      protocol: UDP\n---\n",
            pod_name = pod.name,
        ));
        let metrics_service_name = format!("{}-metrics", pod.name);
        manifest.push_str(&format!(
            "apiVersion: v1\nkind: Service\nmetadata:\n  name: {metrics_service_name}\n  namespace: {namespace}\n  labels:\n    benchmark.stargate/role: pod-metrics\nspec:\n  type: NodePort\n  selector:\n    statefulset.kubernetes.io/pod-name: {pod_name}\n  ports:\n    - name: metrics\n      port: 9090\n      targetPort: metrics\n---\n",
            pod_name = pod.name,
        ));
    }
    manifest
}

pub(super) struct StargatePod {
    pub(super) name: String,
}

pub(super) struct RenderedManifests {
    pub(super) stargate: String,
    pub(super) backends: String,
}

pub(super) struct RenderManifestConfig<'a> {
    pub(super) config: &'a BenchmarkConfig,
    pub(super) algorithm: &'a AlgorithmConfig,
    pub(super) image_refs: &'a ImageRefs,
    pub(super) stargate_ns: &'a str,
    pub(super) backends_ns: &'a str,
    pub(super) lb_config_json: &'a str,
    pub(super) http_node_port: u16,
    pub(super) metrics_node_port: u16,
    pub(super) collector_metrics_node_port: u16,
}

pub(super) fn render_manifest(render: RenderManifestConfig<'_>) -> RenderedManifests {
    let config = render.config;
    let image_refs = render.image_refs;
    let stargate_ns = render.stargate_ns;
    let backends_ns = render.backends_ns;
    let mut stargate = String::new();
    stargate.push_str(&format!(
        "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: {stargate_ns}\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: {backends_ns}\n---\n"
    ));
    stargate.push_str(&format!(
        "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stargate-lb-config\n  namespace: {stargate_ns}\ndata:\n  lb-config.json: |\n"
    ));
    for line in render.lb_config_json.lines() {
        stargate.push_str("    ");
        stargate.push_str(line);
        stargate.push('\n');
    }

    stargate.push_str(&format!(
        "---\napiVersion: v1\nkind: Service\nmetadata:\n  name: stargate\n  namespace: {stargate_ns}\nspec:\n  selector:\n    app: stargate\n  ports:\n    - name: grpc\n      port: 50071\n      targetPort: grpc\n    - name: reverse\n      port: 50072\n      targetPort: reverse\n      protocol: UDP\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: stargate-http\n  namespace: {stargate_ns}\nspec:\n  type: NodePort\n  selector:\n    app: stargate\n  ports:\n    - name: http\n      port: 8000\n      targetPort: http\n      nodePort: {http_node_port}\n    - name: metrics\n      port: 9090\n      targetPort: metrics\n      nodePort: {metrics_node_port}\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: stargate-headless\n  namespace: {stargate_ns}\nspec:\n  clusterIP: None\n  selector:\n    app: stargate\n  ports:\n    - name: http\n      port: 8000\n      targetPort: http\n    - name: metrics\n      port: 9090\n      targetPort: metrics\n    - name: grpc\n      port: 50071\n      targetPort: grpc\n    - name: reverse\n      port: 50072\n      targetPort: reverse\n      protocol: UDP\n---\napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: stargate\n  namespace: {stargate_ns}\nspec:\n  serviceName: stargate-headless\n  replicas: {stargate_count}\n  selector:\n    matchLabels:\n      app: stargate\n  template:\n    metadata:\n      labels:\n        app: stargate\n    spec:\n      containers:\n        - name: stargate\n          image: {stargate_image}\n          imagePullPolicy: IfNotPresent\n          args:\n            - --stargate-id=$(POD_NAME)\n            - --listen-addr=0.0.0.0:50071\n            - --model-discovery-listen-addr=0.0.0.0:50073\n            - --http-listen-addr=0.0.0.0:8000\n            - --advertise-addr=$(POD_IP):50071\n            - --stargate-discovery-dns-name=stargate-headless.{stargate_ns}.svc.cluster.local\n            - --advertised-hostname-template={{pod_name}}-external.{stargate_ns}.svc.cluster.local\n            - --pod-name=$(POD_NAME)\n            - --pod-namespace=$(POD_NAMESPACE)\n            - --metrics-port=9090\n            - --lb-config-path=/config/lb-config.json\n            - --reverse-tunnel-listen-addr=0.0.0.0:50072\n            - --quic-insecure\n            - --tunnel-protocol={tunnel_protocol}\n          env:\n            - name: POD_NAME\n              valueFrom:\n                fieldRef:\n                  fieldPath: metadata.name\n            - name: POD_NAMESPACE\n              valueFrom:\n                fieldRef:\n                  fieldPath: metadata.namespace\n            - name: POD_IP\n              valueFrom:\n                fieldRef:\n                  fieldPath: status.podIP\n          ports:\n            - name: grpc\n              containerPort: 50071\n            - name: model-discovery\n              containerPort: 50073\n            - name: reverse\n              containerPort: 50072\n              protocol: UDP\n            - name: http\n              containerPort: 8000\n            - name: metrics\n              containerPort: 9090\n          readinessProbe:\n            httpGet:\n              path: /readyz\n              port: http\n            initialDelaySeconds: 2\n            periodSeconds: 2\n          livenessProbe:\n            httpGet:\n              path: /healthz\n              port: http\n            initialDelaySeconds: 5\n            periodSeconds: 5\n          volumeMounts:\n            - name: lb-config\n              mountPath: /config\n      volumes:\n        - name: lb-config\n          configMap:\n            name: stargate-lb-config\n---\n",
        http_node_port = render.http_node_port,
        metrics_node_port = render.metrics_node_port,
        stargate_count = config.stargates.count,
        stargate_image = image_refs.stargate,
        tunnel_protocol = config.tunnel_protocol,
    ));
    stargate.push_str(&render_otel_collector(
        stargate_ns,
        backends_ns,
        render.collector_metrics_node_port,
    ));

    let pylon_queue_admission_args = render
        .algorithm
        .pylon_queue_admission
        .as_ref()
        .map(|admission| {
            admission
                .pylon_args()
                .into_iter()
                .map(|arg| format!("            - {arg}\n"))
                .collect::<String>()
        })
        .unwrap_or_default();
    let mut backends = String::new();
    for backend_index in 0..config.backends.count {
        let pylon = PylonRuntimeSpec::for_backend(config, backend_index);
        let cluster_id_arg = pylon
            .cluster_id
            .as_ref()
            .map(|cluster_id| format!("            - --cluster-id={cluster_id}\n"))
            .unwrap_or_default();
        if pylon.owns_upstream_backend() {
            let backend = BackendRuntimeSpec::for_upstream(config, pylon.upstream_index);
            backends.push_str(&format!(
                "apiVersion: v1\nkind: Service\nmetadata:\n  name: {backend_name}-http\n  namespace: {backends_ns}\nspec:\n  selector:\n    app: {backend_name}-inference-server\n  ports:\n    - port: 8090\n      targetPort: http\n      name: http\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: {backend_name}-inference-server\n  namespace: {backends_ns}\nspec:\n  replicas: 1\n  selector:\n    matchLabels:\n      app: {backend_name}-inference-server\n  template:\n    metadata:\n      labels:\n        app: {backend_name}-inference-server\n        benchmark.stargate/profile: {profile_name}\n    spec:\n      containers:\n        - name: inference-server\n          image: {mock_dynamo_image}\n          imagePullPolicy: IfNotPresent\n          args:\n            - --http-listen-addr=0.0.0.0:8090\n            - --model-name={model}\n            - --num-tokens=32\n            - --token-delay-ms={per_token_delay_ms}\n            - --decode-jitter-ms={decode_jitter}\n            - --ttft-ms={ttft}\n            - --ttft-jitter-ms={ttft_jitter}\n            - --prefill-tokens-per-s={prefill_tps}\n            - --max-concurrent-requests={max_concurrent_requests}\n            - --kv-cache-capacity-tokens={kv_cache_capacity_tokens}\n          ports:\n            - containerPort: 8090\n              name: http\n          readinessProbe:\n            httpGet:\n              path: /health\n              port: http\n            initialDelaySeconds: 2\n            periodSeconds: 2\n---\n",
                backend_name = backend.name,
                mock_dynamo_image = image_refs.mock_dynamo,
                model = config.model,
                profile_name = backend.profile_slug,
                per_token_delay_ms = backend.per_token_delay_ms,
                decode_jitter = backend.decode_jitter_ms,
                ttft = backend.ttft_ms,
                ttft_jitter = backend.ttft_jitter_ms,
                prefill_tps = backend.prefill_tokens_per_s,
                max_concurrent_requests = backend.max_concurrent_requests,
                kv_cache_capacity_tokens = backend.kv_cache_capacity_tokens,
            ));
        }
        backends.push_str(&format!(
            "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: {inference_server_id}-pylon\n  namespace: {backends_ns}\nspec:\n  replicas: 1\n  selector:\n    matchLabels:\n      app: {inference_server_id}-pylon\n  template:\n    metadata:\n      labels:\n        app: {inference_server_id}-pylon\n        benchmark.stargate/profile: {profile_name}\n    spec:\n      containers:\n        - name: pylon\n          image: {pylon_image}\n          imagePullPolicy: IfNotPresent\n          args:\n            - --upstream-http-base-url=http://{upstream_backend_name}-http.{backends_ns}.svc.cluster.local:8090\n            - --model-name={model}\n            - --stargate-address=stargate.{stargate_ns}.svc.cluster.local:50071\n            - --inference-server-id={inference_server_id}\n{cluster_id_arg}            - --reverse-tunnel\n            - --quic-insecure\n            - --tunnel-protocol={tunnel_protocol}\n            - --kv-cache-stats-path=/kv-cache/stats\n            - --min-update-interval-ms=100\n            - --disable-bringup\n            - --active-canary-interval-ms=0\n            - --benchmark-fixed-last-mean-input-tps={last_mean_input_tps}\n",
            upstream_backend_name = pylon.upstream_backend_name,
            inference_server_id = pylon.inference_server_id,
            profile_name = pylon.profile_slug,
            pylon_image = image_refs.pylon,
            model = config.model,
            tunnel_protocol = config.tunnel_protocol,
            last_mean_input_tps = pylon.last_mean_input_tps,
        ));
        backends.push_str(&pylon_queue_admission_args);
        backends.push_str("---\n");
    }

    RenderedManifests { stargate, backends }
}

fn render_otel_collector(
    stargate_ns: &str,
    backends_ns: &str,
    collector_metrics_node_port: u16,
) -> String {
    r#"apiVersion: v1
kind: ServiceAccount
metadata:
  name: otel-collector
  namespace: __STARGATE_NS__
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: otel-collector-__STARGATE_NS__
rules:
  - apiGroups: [""]
    resources: ["pods", "namespaces", "endpoints", "services"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: otel-collector-__STARGATE_NS__
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: otel-collector-__STARGATE_NS__
subjects:
  - kind: ServiceAccount
    name: otel-collector
    namespace: __STARGATE_NS__
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector-config
  namespace: __STARGATE_NS__
data:
  otel-collector.yaml: |
    receivers:
      prometheus:
        config:
          scrape_configs:
            - job_name: stargate
              scrape_interval: 1s
              kubernetes_sd_configs:
                - role: pod
                  namespaces:
                    names: ["__STARGATE_NS__"]
              relabel_configs:
                - action: keep
                  source_labels: [__meta_kubernetes_pod_label_app]
                  regex: stargate
                - source_labels: [__meta_kubernetes_pod_ip]
                  target_label: __address__
                  replacement: $1:9090
              metric_relabel_configs:
                - action: keep
                  source_labels: [__name__]
                  regex: stargate_requests_total|stargate_proxy_retries_total|stargate_proxy_retry_exhausted_total|stargate_routing_selections_total|stargate_routing_kv_free_token_fallback_selections_total|stargate_proxy_duration_seconds_.+|stargate_routing_duration_seconds_.+|stargate_active_inference_servers
            - job_name: pylon
              scrape_interval: 1s
              kubernetes_sd_configs:
                - role: pod
                  namespaces:
                    names: ["__BACKENDS_NS__"]
              relabel_configs:
                - action: keep
                  source_labels: [__meta_kubernetes_pod_label_app]
                  regex: backend-.+-pylon
                - source_labels: [__meta_kubernetes_pod_ip]
                  target_label: __address__
                  replacement: $1:9089
              metric_relabel_configs:
                - action: keep
                  source_labels: [__name__]
                  regex: target_info|pylon_requests_total|pylon_.+
    exporters:
      prometheus:
        endpoint: 0.0.0.0:9464
    service:
      pipelines:
        metrics:
          receivers: [prometheus]
          exporters: [prometheus]
---
apiVersion: v1
kind: Service
metadata:
  name: otel-collector
  namespace: __STARGATE_NS__
spec:
  type: NodePort
  selector:
    app: otel-collector
  ports:
    - name: prometheus
      port: 9464
      targetPort: prometheus
      nodePort: __COLLECTOR_METRICS_NODE_PORT__
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel-collector
  namespace: __STARGATE_NS__
spec:
  replicas: 1
  selector:
    matchLabels:
      app: otel-collector
  template:
    metadata:
      labels:
        app: otel-collector
    spec:
      serviceAccountName: otel-collector
      containers:
        - name: otel-collector
          image: otel/opentelemetry-collector-contrib:0.111.0
          imagePullPolicy: IfNotPresent
          args:
            - --config=/conf/otel-collector.yaml
          ports:
            - name: prometheus
              containerPort: 9464
          volumeMounts:
            - name: config
              mountPath: /conf
      volumes:
        - name: config
          configMap:
            name: otel-collector-config
---
"#
    .replace("__STARGATE_NS__", stargate_ns)
    .replace("__BACKENDS_NS__", backends_ns)
    .replace(
        "__COLLECTOR_METRICS_NODE_PORT__",
        &collector_metrics_node_port.to_string(),
    )
}
