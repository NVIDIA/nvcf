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

package util

import (
	"testing"

	olricconfig "github.com/olric-data/olric/config"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
)

// TestConfigureDiscovery_StaticTakesPrecedence locks in that when Peers is
// non-empty we use static even if POD_NAMESPACE is set. Tests set Peers, so
// this path also has to keep working if tests happen to run inside a pod.
func TestConfigureDiscovery_StaticTakesPrecedence(t *testing.T) {
	t.Setenv(PodNamespaceEnv, "default") // shouldn't matter
	oc := olricconfig.New("local")
	mode, err := configureDiscovery(oc, config.OlricConfig{
		Peers:            []string{"10.0.0.1:3320"},
		K8sLabelSelector: "app=x",
	})
	require.NoError(t, err)
	require.Equal(t, "static", mode)
	require.Equal(t, []string{"10.0.0.1:3320"}, oc.Peers)
	require.Nil(t, oc.ServiceDiscovery)
}

// TestConfigureDiscovery_SingleNodeWhenNothingConfigured guards the local-dev
// path: `go run ./cmd/llm-api-gateway` without any env vars or peers should
// form a 1-node cluster. No peers, no plugin.
func TestConfigureDiscovery_SingleNodeWhenNothingConfigured(t *testing.T) {
	t.Setenv(PodNamespaceEnv, "")
	oc := olricconfig.New("local")
	mode, err := configureDiscovery(oc, config.OlricConfig{})
	require.NoError(t, err)
	require.Equal(t, "single-node", mode)
	require.Empty(t, oc.Peers)
	require.Nil(t, oc.ServiceDiscovery)
}

// TestConfigureDiscovery_K8sFailsWithoutInCluster confirms that when we pick
// the k8s branch but aren't actually in a cluster (no service account token
// on disk), we return an error rather than silently degrading. This is the
// only realistic failure we can exercise without a live cluster.
func TestConfigureDiscovery_K8sFailsWithoutInCluster(t *testing.T) {
	t.Setenv(PodNamespaceEnv, "default")
	oc := olricconfig.New("local")
	_, err := configureDiscovery(oc, config.OlricConfig{
		K8sLabelSelector: "app=x",
	})
	require.Error(t, err, "expected InClusterConfig to fail outside a pod")
	require.Contains(t, err.Error(), "kubernetes")
}
