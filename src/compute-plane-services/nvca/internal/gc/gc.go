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

package gc

import (
	"context"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sourcegraph/conc/pool"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	cleanupns "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/gc/namespace"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/gc/persistentvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/gc/pod"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/gc/storageclass"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	bartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	maxConcurrentJobs = 4
	// DefaultInterval is the default interval for the GC controller to run
	DefaultInterval = 1 * time.Hour
)

// Compile-time check to ensure Runnable implements controller-runtime interfaces
var (
	_ manager.Runnable               = (*Runnable)(nil)
	_ manager.LeaderElectionRunnable = (*Runnable)(nil)
)

// Cleaner defines the interface for startup cleaners
type Cleaner interface {
	Name() string
	Run(ctx context.Context) error
}

// Runnable manages garbage collection cleanup operations
type Runnable struct {
	cleaners []Cleaner
	interval time.Duration
}

// NewRunnable creates a new garbage collection controller with the specified interval
// requestsNamespace is the namespace where ICMSRequests are created (defaults to types.DefaultICMSRequestNamespace if empty)
func NewRunnable(clients *kubeclients.KubeClients, m *metrics.Metrics, interval time.Duration, requestsNamespace string) *Runnable {
	icmsGetter := &nvcaICMSRequestGetter{client: clients.BART, icmsRequestNamespace: requestsNamespace}

	// Default to types.DefaultICMSRequestNamespace if not specified
	if requestsNamespace == "" {
		requestsNamespace = types.DefaultICMSRequestNamespace
	}

	return &Runnable{
		cleaners: []Cleaner{
			storageclass.NewCleaner(clients.K8s, icmsGetter, m),
			persistentvolume.NewCleaner(clients.K8s, icmsGetter, m),
			cleanupns.NewCleaner(clients.K8s, clients.BART, icmsGetter, m),
			pod.NewCleaner(clients.K8s, icmsGetter, m, requestsNamespace),
		},
		interval: interval,
	}
}

// Start begins the GC controller, executing cleaners immediately and then at the configured interval
// This implements the controller-runtime Runnable interface
func (gc *Runnable) Start(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Run cleaners immediately on startup
	log.Info("Running GC cleaners immediately")
	gc.runCleaners(ctx)

	// Start ticker for periodic execution
	ticker := time.NewTicker(gc.interval)
	defer ticker.Stop()

	log.Infof("Started GC cleaners with interval %v", gc.interval)

	for {
		select {
		case <-ticker.C:
			log.Debug("Running periodic GC cleaners")
			gc.runCleaners(ctx)
		case <-ctx.Done():
			log.Info("GC controller stopped due to context cancellation")
			return ctx.Err()
		}
	}
}

// NeedLeaderElection indicates whether this Runnable needs to be run in leader election mode
// We return true (the default), but since the manager doesn't have leader election configured,
// this runnable will still run on all instances
func (gc *Runnable) NeedLeaderElection() bool {
	return false
}

// runCleaners executes all cleaners in parallel
func (gc *Runnable) runCleaners(ctx context.Context) {
	log := core.GetLogger(ctx)

	if len(gc.cleaners) == 0 {
		log.Debug("No GC cleaners configured")
		return
	}

	// Create a pool with max concurrent jobs
	p := pool.New().WithMaxGoroutines(maxConcurrentJobs).WithContext(ctx)

	// Add all cleaners to the pool
	for i := range gc.cleaners {
		cleaner := gc.cleaners[i] // capture loop variable
		p.Go(func(ctx context.Context) error {
			if err := cleaner.Run(ctx); err != nil {
				log.WithError(err).Errorf("GC cleaner %s failed", cleaner.Name())
				return err
			}
			return nil
		})
	}

	// Wait for all cleaners to complete
	if err := p.Wait(); err != nil {
		log.WithError(err).Error("Some GC cleaners failed")
	} else {
		log.Debug("All GC cleaners completed successfully")
	}
}

// nvcaICMSRequestGetter implements the ICMSRequestGetter interface using the NVCA client
type nvcaICMSRequestGetter struct {
	client               bartclient.Interface
	icmsRequestNamespace string
}

func (g *nvcaICMSRequestGetter) GetICMSRequest(ctx context.Context, name string) (*nvcav2beta1.ICMSRequest, error) {
	return g.client.NvcaV2beta1().ICMSRequests(g.icmsRequestNamespace).Get(ctx, name, metav1.GetOptions{})
}
