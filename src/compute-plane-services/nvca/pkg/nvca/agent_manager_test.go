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

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func TestStorageControllerTypes(t *testing.T) {
	// Shared-storage and internal-persistent-storage controllers always
	// register; the model-cache controller is gated on caching being enabled.
	base := storageControllerTypes(false)
	assert.Equal(t, []nvcav1new.StorageRequestType{
		nvcav1new.SharedStorageRequest,
		nvcav1new.InternalPersistentStorageRequest,
	}, base, "caching off must not register the model-cache controller")
	assert.NotContains(t, base, nvcav1new.ModelCacheRequest)

	withCache := storageControllerTypes(true)
	assert.Contains(t, withCache, nvcav1new.ModelCacheRequest,
		"caching on must register the model-cache controller")
	assert.Equal(t, []nvcav1new.StorageRequestType{
		nvcav1new.SharedStorageRequest,
		nvcav1new.InternalPersistentStorageRequest,
		nvcav1new.ModelCacheRequest,
	}, withCache)
}
