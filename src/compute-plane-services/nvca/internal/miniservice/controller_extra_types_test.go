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

package mscontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	fakenodefeatures "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/fake"
)

var (
	testCheckpointGVK = schema.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoCheckpoint"}
	testGraphGVK      = schema.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"}
)

// errMapper fails every lookup with err, like a REST mapper whose discovery call failed.
type errMapper struct {
	meta.RESTMapper
	err error
}

func (m errMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, m.err
}

// newExtraTypesTestMapper returns a mapper that serves only the given kinds.
func newExtraTypesTestMapper(served ...schema.GroupVersionKind) meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range served {
		m.Add(gvk, meta.RESTScopeNamespace)
	}
	return m
}

// An allowed extra type whose CRD is not installed must be skipped, not watched. A watch on a
// missing kind never syncs, so the manager times out and the agent exits and crash loops.
func TestPartitionInstalledGVKsSkipsAbsentTypes(t *testing.T) {
	tests := []struct {
		name          string
		served        []schema.GroupVersionKind
		wantInstalled []schema.GroupVersionKind
		wantAbsent    []schema.GroupVersionKind
	}{
		{
			name:          "all installed",
			served:        []schema.GroupVersionKind{testCheckpointGVK, testGraphGVK},
			wantInstalled: []schema.GroupVersionKind{testCheckpointGVK, testGraphGVK},
		},
		{
			name:          "removed CRD is skipped and the rest are kept",
			served:        []schema.GroupVersionKind{testGraphGVK},
			wantInstalled: []schema.GroupVersionKind{testGraphGVK},
			wantAbsent:    []schema.GroupVersionKind{testCheckpointGVK},
		},
		{
			name:       "no CRDs installed",
			wantAbsent: []schema.GroupVersionKind{testCheckpointGVK, testGraphGVK},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installed, absent := partitionInstalledGVKs(newExtraTypesTestMapper(tt.served...),
				[]schema.GroupVersionKind{testCheckpointGVK, testGraphGVK})
			assert.ElementsMatch(t, tt.wantInstalled, installed)
			assert.ElementsMatch(t, tt.wantAbsent, absent)
		})
	}
}

// Only a definite "no such kind" answer may skip a watch. Any other lookup failure, such as a
// transient discovery error, keeps the watch so a type is never dropped on a temporary failure.
func TestPartitionInstalledGVKsKeepsTypeOnLookupError(t *testing.T) {
	installed, absent := partitionInstalledGVKs(
		errMapper{err: errors.New("discovery failed")},
		[]schema.GroupVersionKind{testCheckpointGVK})
	require.Equal(t, []schema.GroupVersionKind{testCheckpointGVK}, installed)
	assert.Empty(t, absent)
}

func TestPartitionInstalledGVKsNoTypes(t *testing.T) {
	installed, absent := partitionInstalledGVKs(newExtraTypesTestMapper(), nil)
	assert.Empty(t, installed)
	assert.Empty(t, absent)
}

// A real API server that serves DynamoGraphDeployment but not DynamoCheckpoint, like a cluster
// running a Dynamo release that removed that CRD. Both types are allowed by the validation
// policy. The controller must start and keep running. Before the absent type was skipped, its
// watch never synced, the controller timed out waiting for caches, and the manager exited, which
// made the agent crash loop.
func TestBuildControllerSkipsWatchOnAbsentAllowedExtraType(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	restConfig, _, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	// Keep the failure fast: an unfixed watch on a missing kind fails after this timeout.
	const cacheSyncTimeout = 3 * time.Second
	skipNameValidation := true
	mgr, err := ctrl.NewManager(restConfig, manager.Options{
		Scheme:                  mgrScheme,
		GracefulShutdownTimeout: new(time.Duration),
		BaseContext:             func() context.Context { return ctx },
		WebhookServer:           nvcaenvtest.NewFakeWebhookServer(),
		Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
		Controller: ctrlconfig.Controller{
			SkipNameValidation: &skipNameValidation,
			CacheSyncTimeout:   cacheSyncTimeout,
		},
	})
	require.NoError(t, err)

	cfg := nvcaconfig.Config{
		Cluster: nvcaconfig.NVCFClusterConfig{
			ValidationPolicy: &nvcaconfig.ValidationPolicyConfig{
				Name: "Default",
				AllowedExtraKubernetesTypes: []nvcaconfig.AllowedExtraKubernetesTypeConfig{
					{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment", Resource: "dynamographdeployments"},
					{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoCheckpoint", Resource: "dynamocheckpoints"},
				},
			},
		},
	}
	require.NoError(t, k8sutil.SetConfigDefaultResources(&cfg))

	regITCache := icms.NewRegistrationInstanceTypeCache()
	opts := ControllerOptions{
		SystemNamespace:      "nvca-system",
		ICMSRequestNamespace: "nvcf-backend",
		K8sVersion:           "1.34.1",
		K8sTimeConfig:        (&k8sutil.TimeConfig{}).Complete(),
		FeatureFlagFetcher:   &featureflagmock.Fetcher{},
		ClusterName:          "local",
		ClusterRegion:        "us-west-1",
		Metrics: metrics.NewDefaultMetrics("nca-cluster", "cluster-foo", "cluster-group-foo", "1.2.3",
			metrics.WithRegisterer(prometheus.NewRegistry())),
		cacheDir: t.TempDir(),
	}
	require.NoError(t, BuildController(ctx, cfg, mgr, newFakeReValClient(t, []byte("[]"), new(error)),
		&fakenodefeatures.Client{}, regITCache, featureflag.NewAttributes(nil), opts))

	mgrErrCh, err := nvcaenvtest.StartManager(ctx, mgr)
	require.NoError(t, err)

	// The manager must outlive the cache sync timeout. An unfixed run reports the failure here.
	select {
	case err := <-mgrErrCh:
		require.NoError(t, err, "manager exited because a watch on an absent allowed type never synced")
		t.Fatal("manager stopped unexpectedly")
	case <-time.After(2 * cacheSyncTimeout):
	}
}
