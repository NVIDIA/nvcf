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

package featureflagmock

import (
	"sync"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

type Fetcher struct {
	EnabledAttrs []*featureflag.Attribute
	EnabledFFs   []*featureflag.FeatureFlag

	mu sync.RWMutex
}

func (f *Fetcher) IsFeatureFlagEnabled(ff *featureflag.FeatureFlag) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, eff := range f.EnabledFFs {
		if eff.Key == ff.Key {
			return true
		}
	}
	return false
}

func (f *Fetcher) IsAttributeEnabled(ff *featureflag.Attribute) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, eff := range f.EnabledAttrs {
		if eff.Key == ff.Key {
			return true
		}
	}
	return false
}

func (f *Fetcher) SetFeatureFlags(ffs ...*featureflag.FeatureFlag) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EnabledFFs = ffs
}

func (f *Fetcher) SetAttributes(attrs ...*featureflag.Attribute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EnabledAttrs = attrs
}
