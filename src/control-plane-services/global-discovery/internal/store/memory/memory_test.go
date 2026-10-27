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

package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return New() })
}

func TestCloseMakesStoreUnavailable(t *testing.T) {
	s := New()
	ctx := context.Background()
	key := store.ClusterKey{ClusterSet: "cs", Cluster: "c1"}
	_, err := s.PutCluster(ctx, store.ClusterRecord{Key: key}, time.Now())
	require.NoError(t, err)

	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "Close is idempotent")

	assert.ErrorIs(t, s.Ping(ctx), store.ErrUnavailable)
	_, err = s.GetCluster(ctx, key)
	assert.ErrorIs(t, err, store.ErrUnavailable)
	_, err = s.PutCluster(ctx, store.ClusterRecord{Key: key}, time.Now())
	assert.ErrorIs(t, err, store.ErrUnavailable)
	_, err = s.ListClusterSets(ctx)
	assert.ErrorIs(t, err, store.ErrUnavailable)
	_, err = s.LoadClusterSet(ctx, "cs")
	assert.ErrorIs(t, err, store.ErrUnavailable)
}
