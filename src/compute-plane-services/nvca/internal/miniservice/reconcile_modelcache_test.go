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
	"testing"

	"github.com/stretchr/testify/assert"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
)

func TestCacheLaunchRequested(t *testing.T) {
	withSize := func(size int64) *nvcav2beta1.ICMSRequest {
		return &nvcav2beta1.ICMSRequest{
			Spec: nvcav2beta1.ICMSRequestSpec{
				CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
					FunctionLaunchSpecification: &function.LaunchSpecification{
						CacheLaunchSpecification: &common.CacheLaunchSpecification{
							CacheHandle: "handle",
							CacheSize:   size,
						},
					},
				},
			},
		}
	}

	assert.True(t, cacheLaunchRequested(withSize(10)), "positive cache size")
	assert.False(t, cacheLaunchRequested(withSize(0)), "zero cache size")
	assert.False(t, cacheLaunchRequested(nil), "nil request")

	noCacheSpec := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}
	assert.False(t, cacheLaunchRequested(noCacheSpec), "no cache spec")
}
