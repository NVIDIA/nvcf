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

package sink

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/labels"
)

const testNS = "byoo-perf"

func TestConfigIsValidYAMLWithOTLPAndDiscard(t *testing.T) {
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(Config()), &parsed); err != nil {
		t.Fatalf("sink config is not valid YAML: %v", err)
	}
	receivers, ok := parsed["receivers"].(map[string]interface{})
	if !ok || receivers["otlp"] == nil {
		t.Fatalf("sink config missing otlp receiver: %#v", parsed["receivers"])
	}
	exporters, ok := parsed["exporters"].(map[string]interface{})
	if !ok || exporters["debug"] == nil {
		t.Fatalf("sink config missing debug exporter: %#v", parsed["exporters"])
	}
	if !strings.Contains(Config(), "health_check") {
		t.Errorf("sink config should enable the health_check extension for readiness")
	}
}

func TestConfigMapCarriesSuiteLabelsAndConfig(t *testing.T) {
	cm := ConfigMap(testNS)
	if cm.Namespace != testNS {
		t.Errorf("namespace = %q, want %q", cm.Namespace, testNS)
	}
	if cm.Labels[labels.PartOf] != labels.PartOfValue {
		t.Errorf("part-of label = %q, want %q", cm.Labels[labels.PartOf], labels.PartOfValue)
	}
	if _, ok := cm.Data[configFileName]; !ok {
		t.Errorf("config map missing key %q", configFileName)
	}
}

func TestPodExposesReceiverAndMetricsPorts(t *testing.T) {
	pod := Pod(testNS, DefaultOptions())
	if got := pod.Spec.Containers[0].Image; got != DefaultImage {
		t.Errorf("image = %q, want %q", got, DefaultImage)
	}
	want := map[string]int32{
		PortNameOTLPGRPC: portOTLPGRPC,
		PortNameOTLPHTTP: portOTLPHTTP,
		PortNameMetrics:  portMetrics,
		portNameHealth:   portHealth,
	}
	got := map[string]int32{}
	for _, p := range pod.Spec.Containers[0].Ports {
		got[p.Name] = p.ContainerPort
	}
	for name, port := range want {
		if got[name] != port {
			t.Errorf("port %q = %d, want %d", name, got[name], port)
		}
	}
	if pod.Spec.Containers[0].ReadinessProbe == nil {
		t.Errorf("sink pod should have a readiness probe")
	}
	if pod.Spec.RestartPolicy != "Always" {
		t.Errorf("restart policy = %q, want Always", pod.Spec.RestartPolicy)
	}
}

func TestPodImageOverride(t *testing.T) {
	pod := Pod(testNS, Options{Image: "example.invalid/sink:testtag"})
	if got := pod.Spec.Containers[0].Image; got != "example.invalid/sink:testtag" {
		t.Errorf("image override not applied: %q", got)
	}
}

func TestServiceSelectsSinkInstance(t *testing.T) {
	svc := Service(testNS)
	if svc.Spec.Selector[labels.Instance] != Name {
		t.Errorf("selector %q = %q, want %q", labels.Instance, svc.Spec.Selector[labels.Instance], Name)
	}
	names := map[string]bool{}
	for _, p := range svc.Spec.Ports {
		names[p.Name] = true
	}
	for _, want := range []string{PortNameOTLPGRPC, PortNameOTLPHTTP, PortNameMetrics} {
		if !names[want] {
			t.Errorf("service missing port %q", want)
		}
	}
}

func TestEndpoints(t *testing.T) {
	if got, want := GRPCEndpoint(testNS), "byoo-perf-otlp-sink.byoo-perf.svc.cluster.local:4317"; got != want {
		t.Errorf("GRPCEndpoint = %q, want %q", got, want)
	}
	if got, want := HTTPEndpoint(testNS), "http://byoo-perf-otlp-sink.byoo-perf.svc.cluster.local:4318"; got != want {
		t.Errorf("HTTPEndpoint = %q, want %q", got, want)
	}
	if got, want := MetricsEndpoint(testNS), "http://byoo-perf-otlp-sink.byoo-perf.svc.cluster.local:8888/metrics"; got != want {
		t.Errorf("MetricsEndpoint = %q, want %q", got, want)
	}
}
