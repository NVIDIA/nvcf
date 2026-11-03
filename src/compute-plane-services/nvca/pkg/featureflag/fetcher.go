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

import "strings"

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

// NewListFetcher returns a Fetcher for an agent configured with the given feature flag
// and attribute lists. It evaluates the lists as the agent does at startup, keeping the
// defaults of unlisted entries and applying implied flags, but leaves the process-wide
// flag state unchanged, so a caller can evaluate an agent configuration it has not
// applied. It returns the error the agent would fail startup with for an invalid
// flag combination.
func NewListFetcher(featureFlags, attributes []string) (Fetcher, error) {
	flagVals := listFlagValues{}
	flagsMutex.Lock()
	err := applyFlags(strings.Join(featureFlags, ","), flagVals, discardLogger{})
	flagsMutex.Unlock()
	if err != nil {
		return nil, err
	}

	attrVals := listAttrValues{}
	attrMu.Lock()
	applyAttributes(strings.Join(attributes, ","), attrVals, discardLogger{})
	attrMu.Unlock()

	return &listFetcher{flags: flagVals, attrs: attrVals}, nil
}

type listFetcher struct {
	flags listFlagValues
	attrs listAttrValues
}

func (f *listFetcher) IsFeatureFlagEnabled(ff *FeatureFlag) bool {
	if ff == nil {
		return false
	}
	return f.flags.enabled(ff)
}

func (f *listFetcher) IsAttributeEnabled(attr *Attribute) bool {
	if attr == nil {
		return false
	}
	return f.attrs.enabled(attr)
}
