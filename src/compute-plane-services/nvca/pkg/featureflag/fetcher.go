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

package featureflag

var _ Fetcher = &defaultFetcher{}

// FeatureFlagFetcher allows for fetching the enabled
// value of the passed in the feature flag. This is
// primarily used for testing where a fetcher
// may be passed into a function that requires
// a feature flag lookup.
//
//nolint:revive
type FeatureFlagFetcher interface {
	IsFeatureFlagEnabled(*FeatureFlag) bool
}

// AttributeFetcher allows for fetching the enabled
// value of the passed in the attribute. This is
// primarily used for testing where a fetcher
// may be passed into a function that requires
// an attribute lookup.
type AttributeFetcher interface {
	IsAttributeEnabled(*Attribute) bool
}

type Fetcher interface {
	FeatureFlagFetcher
	AttributeFetcher
}

var (
	DefaultFetcher = &defaultFetcher{}
)

type defaultFetcher struct {
}

// IsFeatureFlagEnabled returns true if the feature flag is enabled, false if otherwise
func (f *defaultFetcher) IsFeatureFlagEnabled(ff *FeatureFlag) bool {
	if ff == nil {
		return false
	}
	return ff.Enabled()
}

// IsAttributeEnabled returns true if the feature flag is enabled, false if otherwise
func (f *defaultFetcher) IsAttributeEnabled(ff *Attribute) bool {
	if ff == nil {
		return false
	}
	return ff.Enabled()
}
