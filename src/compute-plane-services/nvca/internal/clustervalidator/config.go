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

package clustervalidator

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const configDataKey = "config.yaml"

// NetworkCheckConfig is the top-level configuration parsed from the
// cluster-validator ConfigMap.
type NetworkCheckConfig struct {
	Reachability    *ReachabilityConfig    `json:"reachability,omitempty"`
	NetworkPolicies *NetworkPoliciesConfig `json:"networkPolicies,omitempty"`
	Enforcement     *EnforcementConfig     `json:"enforcement,omitempty"`
}

// EnforcementConfig controls active NetworkPolicy enforcement testing.
// When enabled, ephemeral pods are deployed in a temporary namespace to
// verify the CNI data-plane actually enforces policies.
// When Critical is true, an enforcement failure marks the cluster as not-ready.
type EnforcementConfig struct {
	Enabled        bool   `json:"enabled"`
	TestImage      string `json:"testImage,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	Critical       bool   `json:"critical,omitempty"`
}

// ReachabilityConfig holds user-defined endpoints for live connectivity probes.
type ReachabilityConfig struct {
	Endpoints []ReachabilityEndpoint `json:"endpoints"`
}

// ReachabilityEndpoint describes a single endpoint to probe.
// When the ConfigMap reachability section replaces the built-in endpoint
// checks, Critical controls whether a failure is cluster-blocking (true) or
// just a warning (false, the default).
type ReachabilityEndpoint struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	URL      string `json:"url,omitempty"`
	Critical bool   `json:"critical,omitempty"`
}

// NetworkPoliciesConfig holds pairs of namespaces whose NetworkPolicy objects
// should be inspected for bidirectional (A→B, B→A) coverage.
type NetworkPoliciesConfig struct {
	Pairs []NetworkPolicyPair `json:"pairs"`
}

// NetworkPolicyPair describes two namespace endpoints and the port/protocol
// that traffic should be allowed on.
// When Critical is true, a failure for this pair marks the cluster as not-ready
// instead of just producing a warning.
type NetworkPolicyPair struct {
	Name     string                `json:"name"`
	A        NetworkPolicyEndpoint `json:"a"`
	B        NetworkPolicyEndpoint `json:"b"`
	Port     int                   `json:"port"`
	Protocol string                `json:"protocol"`
	Critical bool                  `json:"critical,omitempty"`
}

// NetworkPolicyEndpoint identifies a set of pods in a namespace.
type NetworkPolicyEndpoint struct {
	Namespace   string            `json:"namespace"`
	PodSelector map[string]string `json:"podSelector,omitempty"`
}

var allowedReachabilityProtocols = map[string]bool{
	"https":   true,
	"tcp":     true,
	"tcp+tls": true,
}

var allowedNetPolProtocols = map[string]bool{
	"TCP": true,
	"UDP": true,
}

// LoadNetworkCheckConfig reads and parses the cluster-validator ConfigMap.
// It returns (nil, nil) when the ConfigMap does not exist, allowing callers
// to silently skip configurable checks.
func LoadNetworkCheckConfig(ctx context.Context, client kubernetes.Interface, namespace, name string) (*NetworkCheckConfig, error) {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading ConfigMap %s/%s: %w", namespace, name, err)
	}

	raw, ok := cm.Data[configDataKey]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s/%s missing key %q", namespace, name, configDataKey)
	}

	var cfg NetworkCheckConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("parsing ConfigMap %s/%s: %w", namespace, name, err)
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config in %s/%s: %w", namespace, name, err)
	}

	return &cfg, nil
}

func validateConfig(cfg *NetworkCheckConfig) error {
	if cfg.Reachability != nil {
		for i, ep := range cfg.Reachability.Endpoints {
			if ep.Name == "" {
				return fmt.Errorf("reachability.endpoints[%d]: name is required", i)
			}
			if ep.Host == "" && ep.URL == "" {
				return fmt.Errorf("reachability.endpoints[%d] %q: host or url is required", i, ep.Name)
			}
			if ep.Port < 0 || ep.Port > 65535 {
				return fmt.Errorf("reachability.endpoints[%d] %q: port must be 0-65535", i, ep.Name)
			}
			if ep.Protocol == "" {
				return fmt.Errorf("reachability.endpoints[%d] %q: protocol is required", i, ep.Name)
			}
			if !allowedReachabilityProtocols[strings.ToLower(ep.Protocol)] {
				return fmt.Errorf("reachability.endpoints[%d] %q: unsupported protocol %q (use https, tcp, or tcp+tls)", i, ep.Name, ep.Protocol)
			}
		}
	}

	if cfg.Enforcement != nil {
		if cfg.Enforcement.TestImage != "" && strings.ContainsAny(cfg.Enforcement.TestImage, " \t\n") {
			return fmt.Errorf("enforcement.testImage: must not contain whitespace")
		}
		if cfg.Enforcement.TimeoutSeconds < 0 {
			return fmt.Errorf("enforcement.timeoutSeconds: must be non-negative")
		}
	}

	if cfg.NetworkPolicies != nil {
		for i, pair := range cfg.NetworkPolicies.Pairs {
			if pair.Name == "" {
				return fmt.Errorf("networkPolicies.pairs[%d]: name is required", i)
			}
			if pair.A.Namespace == "" {
				return fmt.Errorf("networkPolicies.pairs[%d] %q: a.namespace is required", i, pair.Name)
			}
			if pair.B.Namespace == "" {
				return fmt.Errorf("networkPolicies.pairs[%d] %q: b.namespace is required", i, pair.Name)
			}
			if pair.Port <= 0 || pair.Port > 65535 {
				return fmt.Errorf("networkPolicies.pairs[%d] %q: port must be 1-65535", i, pair.Name)
			}
			proto := strings.ToUpper(pair.Protocol)
			if proto == "" {
				return fmt.Errorf("networkPolicies.pairs[%d] %q: protocol is required", i, pair.Name)
			}
			if !allowedNetPolProtocols[proto] {
				return fmt.Errorf("networkPolicies.pairs[%d] %q: unsupported protocol %q (use TCP or UDP)", i, pair.Name, pair.Protocol)
			}
		}
	}

	return nil
}
