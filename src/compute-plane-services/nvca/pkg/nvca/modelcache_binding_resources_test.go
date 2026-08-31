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

package nvca

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

// TestRegularModelCacheTargetClaim pins which claim drives regular model
// caching for each shape. The ROX shape waits on a reader claim derived from
// the writer volume; every other shape publishes the writer claim itself and
// readers mount it read-only, so there is no second claim to wait for.
func TestRegularModelCacheTargetClaim(t *testing.T) {
	bindingFor := func(transition string) *nvcav2beta1.ModelCacheBinding {
		return &nvcav2beta1.ModelCacheBinding{
			Spec: nvcav2beta1.ModelCacheBindingSpec{
				Decision: nvcav2beta1.ModelCacheBindingDecision{Transition: transition},
			},
		}
	}
	const writer = "rw-pvc-handle"

	t.Run("the ROX shape waits on its own reader claim", func(t *testing.T) {
		name, separate, err := regularModelCacheTargetClaim(
			bindingFor(nvcastorage.ModelCacheTransitionROXReadOnly), writer)
		require.NoError(t, err)
		assert.True(t, separate)
		wantReader, err := regularModelCacheReaderPVCName(writer)
		require.NoError(t, err)
		assert.Equal(t, wantReader, name)
		assert.NotEqual(t, writer, name)
	})

	t.Run("the RWX shape waits on the writer claim", func(t *testing.T) {
		name, separate, err := regularModelCacheTargetClaim(
			bindingFor(nvcastorage.ModelCacheTransitionRWXReadOnly), writer)
		require.NoError(t, err)
		assert.False(t, separate)
		assert.Equal(t, writer, name,
			"the writer claim is the reader claim, mounted read-only")
	})

	t.Run("no binding keeps the legacy ROX behaviour", func(t *testing.T) {
		name, separate, err := regularModelCacheTargetClaim(nil, writer)
		require.NoError(t, err)
		assert.True(t, separate)
		wantReader, err := regularModelCacheReaderPVCName(writer)
		require.NoError(t, err)
		assert.Equal(t, wantReader, name)
	})

	t.Run("an unknown transition is refused, not defaulted", func(t *testing.T) {
		_, _, err := regularModelCacheTargetClaim(bindingFor("someFutureShape"), writer)
		require.Error(t, err)
	})
}
