/*
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
*/

// Package byoofederation checks that the configured BYOO platform metrics
// federation target answers Prometheus /federate requests.
package byoofederation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// ComponentName is the component health key for the federation check.
	ComponentName = "byoo-platform-metrics-federation"

	// DefaultInterval is how often Run probes the target after the first probe.
	DefaultInterval = 5 * time.Minute

	// probeTimeout bounds one probe, so a target that drops packets fails
	// the check instead of holding it.
	probeTimeout = 10 * time.Second

	// probeSelector matches no series, so the probe exercises /federate
	// without transferring data.
	probeSelector = `{__name__="nvca_byoo_federation_probe"}`

	maxDrainBytes = 4096
)

// Target returns the target NVCA checks: the configured target when
// federation is not disabled, otherwise "". An unset target means the
// collector default, which NVCA does not check.
func Target(cfg nvcaconfig.BYOOOTelPlatformMetricsFederationConfig) string {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return ""
	}
	return cfg.Target
}

// StateRecorder records the result of each probe.
type StateRecorder interface {
	SetBYOOPlatformMetricsFederationState(state nvcametrics.BYOOPlatformMetricsFederationState)
}

// Checker probes the federation target in the background and reports the
// latest result as a warn-level component status. A failed probe never makes
// NVCA unhealthy or blocks workload deployments.
type Checker struct {
	target   string
	probeURL string
	client   *http.Client
	interval time.Duration
	recorder StateRecorder

	mu      sync.RWMutex
	checked bool
	lastErr error
}

var _ health.ComponentStatusGetter = (*Checker)(nil)

// Option configures a Checker.
type Option func(*Checker)

// WithHTTPClient replaces the HTTP client used for probes.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Checker) {
		c.client = client
	}
}

// WithInterval replaces DefaultInterval.
func WithInterval(interval time.Duration) Option {
	return func(c *Checker) {
		c.interval = interval
	}
}

// NewChecker returns a Checker for target, a host:port that serves /federate
// over HTTP. recorder may be nil.
func NewChecker(target string, recorder StateRecorder, opts ...Option) *Checker {
	query := url.Values{}
	query.Set("match[]", probeSelector)
	probeURL := url.URL{Scheme: "http", Host: target, Path: "/federate", RawQuery: query.Encode()}

	// The collector scrapes the target directly, so the probe ignores
	// proxy environment variables to test the same path.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	c := &Checker{
		target:   target,
		probeURL: probeURL.String(),
		client:   &http.Client{Timeout: probeTimeout, Transport: transport},
		interval: DefaultInterval,
		recorder: recorder,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Run probes the target immediately and then every interval until ctx is done.
func (c *Checker) Run(ctx context.Context) {
	log := core.GetLogger(ctx).WithField("component", ComponentName)
	log.Infof("Checking BYOO platform metrics federation target %s every %s", c.target, c.interval)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		c.Probe(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Probe checks the target once, records the result, and logs state changes.
func (c *Checker) Probe(ctx context.Context) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"component": ComponentName,
		"target":    c.target,
	})

	err := c.probe(ctx)
	if ctx.Err() != nil {
		// Shutting down: keep the last result.
		return err
	}

	c.mu.Lock()
	firstProbe := !c.checked
	wasReachable := c.checked && c.lastErr == nil
	c.checked = true
	c.lastErr = err
	c.mu.Unlock()

	if err != nil {
		if c.recorder != nil {
			c.recorder.SetBYOOPlatformMetricsFederationState(nvcametrics.BYOOPlatformMetricsFederationUnreachable)
		}
		if firstProbe || wasReachable {
			log.WithError(err).Warn(unreachableMessage(c.target, err))
		} else {
			log.WithError(err).Debug("BYOO platform metrics federation target is still unreachable")
		}
		return err
	}

	if c.recorder != nil {
		c.recorder.SetBYOOPlatformMetricsFederationState(nvcametrics.BYOOPlatformMetricsFederationReachable)
	}
	if !wasReachable {
		log.Info("BYOO platform metrics federation target is reachable")
	}
	return nil
}

func (c *Checker) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.probeURL, nil)
	if err != nil {
		return fmt.Errorf("build /federate request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET /federate: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("GET /federate returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// GetComponentStatus reports the latest probe result. It never returns an
// error, and the component is warn-level, so an unreachable target does not
// make NVCA unhealthy or stop it from accepting workloads.
func (c *Checker) GetComponentStatus(context.Context) (nvcatypes.AgentHealth, error) {
	c.mu.RLock()
	lastErr := c.lastErr
	c.mu.RUnlock()

	component := nvcatypes.ComponentHealth{
		Status:      nvcatypes.HealthStatusHealthy,
		StatusLevel: nvcatypes.StatusLevelWarn,
	}
	if lastErr != nil {
		component.Status = nvcatypes.HealthStatusUnhealthy
		component.Errors = []string{unreachableMessage(c.target, lastErr)}
	}
	return nvcatypes.AgentHealth{
		Status:     nvcatypes.HealthStatusHealthy,
		Components: map[string]nvcatypes.ComponentHealth{ComponentName: component},
	}, nil
}

func unreachableMessage(target string, err error) string {
	return fmt.Sprintf("BYOO platform metrics federation target %s is unreachable: %v. "+
		"BYOO collectors cannot export platform metrics and report up=0 for their federation scrape jobs; "+
		"workloads still deploy. Set byoo.otelCollector.platformMetricsFederation.target to a reachable "+
		"Prometheus-compatible server that serves /federate, or set "+
		"byoo.otelCollector.platformMetricsFederation.enabled to false.", target, err)
}
