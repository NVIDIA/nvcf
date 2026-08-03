/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
*/

// Package sink builds an in-cluster OTLP sink: a stock OpenTelemetry Collector
// (contrib) that accepts OTLP over gRPC and HTTP, discards the payloads, and
// exposes its own receiver counters on a Prometheus endpoint. It is the
// destination the BYOO collector under test exports to, so the suite can drive
// steady-state load without a real external backend and later read how much
// telemetry actually made it through.
package sink

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/labels"
)

const (
	// DefaultImage is a stock upstream collector-contrib build. It only needs
	// the otlp receiver and a discard exporter, so any recent contrib tag
	// works; this one matches the tag used elsewhere in the repo.
	DefaultImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.129.1"

	// Name is the object name for the sink pod, service, and config map.
	Name = "byoo-perf-otlp-sink"

	// configVolumeName / configMountPath locate the mounted sink config.
	configVolumeName = "sink-config"
	configMountPath  = "/etc/otel-sink"
	configFileName   = "config.yaml"

	// Ports the sink exposes.
	portOTLPGRPC = 4317
	portOTLPHTTP = 4318
	portMetrics  = 8888
	portHealth   = 13133

	// MetricsPort is the sink's Prometheus telemetry port, exported so callers
	// can scrape its receiver counters through the API-server proxy.
	MetricsPort = portMetrics

	// Port names, reused as the harness endpoint keys.
	PortNameOTLPGRPC = "otlp-grpc"
	PortNameOTLPHTTP = "otlp-http"
	PortNameMetrics  = "metrics"
	portNameHealth   = "health"
)

// Options controls the sink deployment.
type Options struct {
	// Image is the collector-contrib image used as the sink.
	Image string
}

// DefaultOptions returns the standard sink options.
func DefaultOptions() Options {
	return Options{Image: DefaultImage}
}

// Config is the sink collector's YAML configuration: accept OTLP on all
// interfaces, discard via the debug exporter, expose receiver counters on the
// Prometheus telemetry endpoint, and gate readiness with the health_check
// extension.
func Config() string {
	return fmt.Sprintf(`receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:%d
      http:
        endpoint: 0.0.0.0:%d
exporters:
  debug:
    verbosity: basic
extensions:
  health_check:
    endpoint: 0.0.0.0:%d
service:
  extensions: [health_check]
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: 0.0.0.0
                port: %d
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [debug]
    metrics:
      receivers: [otlp]
      exporters: [debug]
    traces:
      receivers: [otlp]
      exporters: [debug]
`, portOTLPGRPC, portOTLPHTTP, portHealth, portMetrics)
}

func objectMeta(namespace string) metav1.ObjectMeta {
	l := labels.Base()
	l[labels.Instance] = Name
	l[labels.Component] = labels.ComponentSink
	return metav1.ObjectMeta{Name: Name, Namespace: namespace, Labels: l}
}

// ConfigMap holds the sink collector config.
func ConfigMap(namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: objectMeta(namespace),
		Data:       map[string]string{configFileName: Config()},
	}
}

// Pod is the sink collector pod.
func Pod(namespace string, opts Options) *corev1.Pod {
	image := opts.Image
	if image == "" {
		image = DefaultImage
	}
	return &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: objectMeta(namespace),
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "otlp-sink",
				Image: image,
				Args:  []string{fmt.Sprintf("--config=%s/%s", configMountPath, configFileName)},
				Ports: []corev1.ContainerPort{
					{Name: PortNameOTLPGRPC, ContainerPort: portOTLPGRPC, Protocol: corev1.ProtocolTCP},
					{Name: PortNameOTLPHTTP, ContainerPort: portOTLPHTTP, Protocol: corev1.ProtocolTCP},
					{Name: PortNameMetrics, ContainerPort: portMetrics, Protocol: corev1.ProtocolTCP},
					{Name: portNameHealth, ContainerPort: portHealth, Protocol: corev1.ProtocolTCP},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/",
							Port: intstr.FromString(portNameHealth),
						},
					},
					InitialDelaySeconds: 2,
					PeriodSeconds:       2,
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      configVolumeName,
					MountPath: configMountPath,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: configVolumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: Name},
					},
				},
			}},
		},
	}
}

// Service is a ClusterIP Service that fronts the sink pod and gives the BYOO
// collector a stable in-cluster address to export to.
func Service(namespace string) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{Kind: "Service", APIVersion: "v1"},
		ObjectMeta: objectMeta(namespace),
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{labels.Instance: Name},
			Ports: []corev1.ServicePort{
				{Name: PortNameOTLPGRPC, Port: portOTLPGRPC, TargetPort: intstr.FromString(PortNameOTLPGRPC), Protocol: corev1.ProtocolTCP},
				{Name: PortNameOTLPHTTP, Port: portOTLPHTTP, TargetPort: intstr.FromString(PortNameOTLPHTTP), Protocol: corev1.ProtocolTCP},
				{Name: PortNameMetrics, Port: portMetrics, TargetPort: intstr.FromString(PortNameMetrics), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// GRPCEndpoint returns the in-cluster host:port for the sink's OTLP gRPC
// receiver.
func GRPCEndpoint(namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", Name, namespace, portOTLPGRPC)
}

// HTTPEndpoint returns the in-cluster base URL for the sink's OTLP HTTP
// receiver. The BYOO collector's otlp_http exporter appends the signal path
// (e.g. /v1/logs) to it.
func HTTPEndpoint(namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", Name, namespace, portOTLPHTTP)
}

// MetricsEndpoint returns the in-cluster URL of the sink's Prometheus telemetry
// endpoint, where its otelcol_receiver_accepted_* counters are exposed.
func MetricsEndpoint(namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/metrics", Name, namespace, portMetrics)
}
