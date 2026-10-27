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

package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamesAndPreinitialisation(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.SetBuildInfo("v1", "abc")

	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
		assert.True(t, strings.HasPrefix(f.GetName(), "nvcf_gds_"), f.GetName())
	}
	for _, n := range []string{
		"nvcf_gds_lease_expirations_total", "nvcf_gds_watch_events_sent_total",
		"nvcf_gds_watch_resync_total", "nvcf_gds_watch_rejected_total",
		"nvcf_gds_view_refresh_failures_total", "nvcf_gds_store_conditional_failures_total",
		"nvcf_gds_compaction_deleted_total", "nvcf_gds_build_info", "nvcf_gds_watchers",
		"nvcf_gds_store_request_duration_seconds",
	} {
		assert.True(t, names[n], "%s is missing on the first scrape", n)
	}
	assert.Equal(t, len(Kinds)*len(EventTypes), testutil.CollectAndCount(m.WatchEventsSent))
	assert.Equal(t, len(Kinds)*len(ResyncReasons), testutil.CollectAndCount(m.WatchResyncs))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.BuildInfo.WithLabelValues("v1", "abc")))
}

func TestClusterSetGauges(t *testing.T) {
	m := New(prometheus.NewRegistry())
	m.SetClusterSet("prod", ClusterSetGauges{
		ActiveClusters: 2, ExpiredClusters: 1, Exports: 5, ReadyEndpoints: 7, NotReadyEnds: 1,
		Imports: 3, ConflictedImports: 1, PoolsByState: map[string]int{"ACTIVE": 2}, Routers: 1,
	})
	assert.Equal(t, 2.0, testutil.ToFloat64(m.Clusters.WithLabelValues("prod", "active")))
	assert.Equal(t, 2.0, testutil.ToFloat64(m.ServiceImports.WithLabelValues("prod", "false")))
	assert.Equal(t, 0.0, testutil.ToFloat64(m.Pools.WithLabelValues("prod", "DRAINING")))
	assert.Equal(t, 2.0, testutil.ToFloat64(m.Pools.WithLabelValues("prod", "ACTIVE")))
	m.DeleteClusterSet("prod")
	assert.Equal(t, 0, testutil.CollectAndCount(m.Clusters))
	assert.Equal(t, 0, testutil.CollectAndCount(m.Pools))
}
