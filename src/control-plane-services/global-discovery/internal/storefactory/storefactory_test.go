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

package storefactory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/memory"
)

func TestOpen(t *testing.T) {
	cfg := config.Default()
	st, err := Open(context.Background(), cfg)
	require.NoError(t, err)
	assert.IsType(t, &memory.Store{}, st)

	cfg.Store = config.StoreDynamoDB
	_, err = Open(context.Background(), cfg)
	assert.ErrorContains(t, err, "not built into this binary yet")

	cfg.Store = "etcd"
	_, err = Open(context.Background(), cfg)
	assert.ErrorContains(t, err, "unknown store")

	Register("test", func(context.Context, *config.Config) (store.Store, error) { return memory.New(), nil })
	t.Cleanup(func() {
		mu.Lock()
		delete(openers, "test")
		mu.Unlock()
	})
	assert.Equal(t, []string{"memory", "test"}, Kinds())
	cfg.Store = "test"
	_, err = Open(context.Background(), cfg)
	require.NoError(t, err)
}
