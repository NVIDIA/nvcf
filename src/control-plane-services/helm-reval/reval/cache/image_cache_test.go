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

package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestImageCache(t *testing.T) {
	ic := NewImageCache(zap.NewExample())

	imageTag1 := "foo"
	imageTag2 := "bar"

	// With public.
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	}))
	ic.Delete(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	})
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	}))
	ic.Put(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	})
	assert.True(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	}))
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag2,
		Public:   true,
	}))
	ic.Delete(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	})
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
		Public:   true,
	}))

	// Without public.
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
	}))
	ic.Delete(ImageCacheInput{
		ImageTag: imageTag1,
	})
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
	}))
	ic.Put(ImageCacheInput{
		ImageTag: imageTag1,
	})
	assert.False(t, ic.Get(ImageCacheInput{
		ImageTag: imageTag1,
	}))
}
