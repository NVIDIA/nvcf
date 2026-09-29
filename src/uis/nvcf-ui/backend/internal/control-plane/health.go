// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/utils"
)

var (
	// healthCheckInterval is how often RunHealthChecks probes every component. It's
	// a package variable so tests can shorten the otherwise 20s cycle.
	healthCheckInterval = 20 * time.Second

	controlPlaneHealthCM = "control-plane-health"
	namespaceEnvVar      = "NAMESPACE"
	defaultNamespace     = "nvcf-ui"
)

// configMapNamespace resolves the namespace holding the control-plane health
// ConfigMap, from namespaceEnvVar or defaultNamespace.
func configMapNamespace() string {
	return utils.GetEnvOr(namespaceEnvVar, defaultNamespace)
}

type healthStatus string

const (
	healthStatusHealthy   healthStatus = "healthy"
	healthStatusUnhealthy healthStatus = "unhealthy"
)

type k8sWorkload struct {
	Type string         `yaml:"type"`
	Name string         `yaml:"name"`
	obj  workloadObject // instantiated at load time based on Type
}

// componentConfig is one entry in the components list. A component is probed
// either over HTTP (Endpoints) or by inspecting Kubernetes workloads
// (K8sWorkloads) — exactly one of the two must be set.
type componentConfig struct {
	Name         string        `yaml:"name"`
	Namespace    string        `yaml:"namespace"`
	Endpoints    []string      `yaml:"endpoints"`
	K8sWorkloads []k8sWorkload `yaml:"k8s_workloads"`
}

type componentsConfig struct {
	Components []componentConfig `yaml:"components"`
}

// --- Runtime health tracking ---

type componentHealth struct {
	ComponentName string
	Namespace     string // namespace the component lives in; used for k8s workload lookups and reported in the status response
	Status        healthStatus
	Timestamp     time.Time
	endpoints     []string      // HTTP endpoints; empty for k8s components
	workloads     []k8sWorkload // k8s workloads; empty for HTTP components
}

// Monitor tracks and reports the health of the control-plane components.
// Its components slice is populated once by Loadcomponents before
// RunHealthChecks starts; each health-check goroutine then writes only its own
// distinct index, so concurrent status updates are race-free without locking.
type Monitor struct {
	k8sClient  client.Client
	httpClient *http.Client
	components []componentHealth
}

// statusEntry is the per-component value stored in the ConfigMap data,
// keyed by component name.
type statusEntry struct {
	Namespace string       `yaml:"namespace"`
	Status    healthStatus `yaml:"status"`
	Timestamp time.Time    `yaml:"timestamp"`
}

// New returns a Monitor that reaches the cluster through k8sClient and probes
// HTTP endpoints with a fixed-timeout client.
func New(k8sClient client.Client) *Monitor {
	return &Monitor{
		k8sClient:  k8sClient,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (m *Monitor) Loadcomponents(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}

	var cfg componentsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse components config: %w", err)
	}

	for _, c := range cfg.Components {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return fmt.Errorf("missing name in components entry")
		}
		namespace := strings.TrimSpace(c.Namespace)
		if namespace == "" {
			return fmt.Errorf("component %q: missing namespace", name)
		}

		// A component is checked over HTTP, via k8s workloads, or both — but it
		// must define at least one of the two.
		hasEndpoints := len(c.Endpoints) > 0
		hasWorkloads := len(c.K8sWorkloads) > 0
		if !hasEndpoints && !hasWorkloads {
			return fmt.Errorf("component %q: at least one of endpoints or k8s_workloads must be set", name)
		}

		comp := componentHealth{
			ComponentName: name,
			Namespace:     namespace,
			Status:        healthStatusUnhealthy,
			Timestamp:     time.Now().UTC(),
		}

		if hasEndpoints {
			endpoints := make([]string, 0, len(c.Endpoints))
			for _, ep := range c.Endpoints {
				ep = strings.TrimSpace(ep)
				if err := validateFQDN(ep); err != nil {
					return fmt.Errorf("component %q: %w", name, err)
				}
				endpoints = append(endpoints, "http://"+ep)
			}
			comp.endpoints = endpoints
		}

		if hasWorkloads {
			workloads := make([]k8sWorkload, 0, len(c.K8sWorkloads))
			for _, w := range c.K8sWorkloads {
				if strings.TrimSpace(w.Name) == "" {
					return fmt.Errorf("component %q: missing workload name", name)
				}
				t := strings.ToLower(strings.TrimSpace(w.Type))
				if !validK8sWorkloadTypes[t] {
					return fmt.Errorf("component %q: invalid workload type %q, must be one of: deployment, statefulset, daemonset, replicaset", name, w.Type)
				}
				var obj workloadObject
				switch t {
				case "deployment":
					obj = &deploymentWorkload{}
				case "statefulset":
					obj = &statefulSetWorkload{}
				case "daemonset":
					obj = &daemonSetWorkload{}
				case "replicaset":
					obj = &replicaSetWorkload{}
				}
				workloads = append(workloads, k8sWorkload{Name: w.Name, Type: t, obj: obj})
			}
			comp.workloads = workloads
		}

		m.components = append(m.components, comp)
	}

	return nil
}

func (m *Monitor) checkComponentHealth(ctx context.Context, index int) {
	logger := zerolog.Ctx(ctx)

	// A component may define endpoints, workloads, or both; it is healthy only
	// when every configured check passes.
	var err error
	if len(m.components[index].endpoints) > 0 {
		err = m.checkEndpoints(ctx, index)
	}
	if err == nil && len(m.components[index].workloads) > 0 {
		err = m.checkWorkloads(ctx, index)
	}

	newStatus := healthStatusHealthy
	if err != nil {
		newStatus = healthStatusUnhealthy
	}
	if newStatus == m.components[index].Status {
		return // no transition; nothing to log or update
	}

	if err != nil {
		logger.Error().Err(err).Msgf("component %s is unhealthy", m.components[index].ComponentName)
	} else {
		logger.Info().Msgf("component %s is healthy", m.components[index].ComponentName)
	}
	m.components[index].Status = newStatus
	m.components[index].Timestamp = time.Now().UTC()
}

// saveStatusToConfigMap writes the current component statuses to the named
// ConfigMap, with one data key per component. It uses server-side apply so a
// single call creates or fully overwrites the ConfigMap with the in-memory
// values — any manual edits are clobbered on the next run.
func (m *Monitor) saveStatusToConfigMap(ctx context.Context, namespace, name string) error {
	data := make(map[string]string, len(m.components))
	for _, c := range m.components {
		out, err := yaml.Marshal(statusEntry{Namespace: c.Namespace, Status: c.Status, Timestamp: c.Timestamp})
		if err != nil {
			return fmt.Errorf("marshal status for %s: %w", c.ComponentName, err)
		}
		data[c.ComponentName] = string(out)
	}

	cm := corev1ac.ConfigMap(name, namespace).WithData(data)
	return m.k8sClient.Apply(ctx, cm, client.FieldOwner("backend"), client.ForceOwnership)
}

// seedFromConfigMap restores each component's last-known Status and Timestamp
// from the existing health ConfigMap. Without this, a pod restart re-initializes
// every component as unhealthy (see Loadcomponents), so the first health check
// looks like a transition and stamps time.Now() — resetting the "status held
// since" timestamp of components that never actually changed state.
//
// Only Status and Timestamp are seeded; the rest of each component (endpoints,
// workloads, namespace) always comes from the current config. Entries in the
// ConfigMap that no longer match a configured component are ignored, and a
// missing ConfigMap (first-ever run) is not an error.
func (m *Monitor) seedFromConfigMap(ctx context.Context, namespace, name string) error {
	if len(m.components) == 0 {
		return nil // nothing to restore
	}

	var cm corev1.ConfigMap
	nn := types.NamespacedName{Namespace: namespace, Name: name}
	if err := m.k8sClient.Get(ctx, nn, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("read health configmap %s/%s: %w", namespace, name, err)
	}

	for i := range m.components {
		raw, ok := cm.Data[m.components[i].ComponentName]
		if !ok {
			continue
		}
		var entry statusEntry
		if err := yaml.Unmarshal([]byte(raw), &entry); err != nil {
			// A malformed entry just means we fall back to the fresh default for
			// this component; log via caller context, don't fail the whole seed.
			continue
		}
		if entry.Status != healthStatusHealthy && entry.Status != healthStatusUnhealthy {
			continue
		}
		m.components[i].Status = entry.Status
		m.components[i].Timestamp = entry.Timestamp
	}

	return nil
}

func (m *Monitor) RunHealthChecks(ctx context.Context) {
	logger := zerolog.Ctx(ctx)
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()
	cmNamespace := configMapNamespace()
	hbPath := heartbeatPath()

	// Restore last-known statuses so a restart doesn't reset the timestamp of
	// components whose health hasn't actually changed. Best-effort: on failure
	// we keep the fresh defaults and log.
	if err := m.seedFromConfigMap(ctx, cmNamespace, controlPlaneHealthCM); err != nil {
		logger.Error().Err(err).Msg("failed to seed health status from configmap")
	}

	// Beat once on entry so the liveness probe passes during the first interval,
	// before any tick has fired.
	if err := writeHeartbeat(hbPath); err != nil {
		logger.Error().Err(err).Msg("failed to write initial heartbeat")
	}

	var (
		wg  sync.WaitGroup
		err error
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := range m.components {
				wg.Go(func() {
					m.checkComponentHealth(ctx, i)
				})
			}
			wg.Wait()

			if err = m.saveStatusToConfigMap(ctx, cmNamespace, controlPlaneHealthCM); err != nil {
				logger.Error().Err(err).Msg("failed to save health status to configmap")
			}

			// A completed cycle is the monitor's proof of life: refresh the
			// heartbeat the liveness probe reads. We beat even when the
			// ConfigMap write failed — the loop is still ticking, which is what
			// liveness measures; the persist error is logged separately.
			if err = writeHeartbeat(hbPath); err != nil {
				logger.Error().Err(err).Msg("failed to write heartbeat")
			}
		}
	}
}
