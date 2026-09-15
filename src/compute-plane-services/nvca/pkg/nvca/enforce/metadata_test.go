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

package enforce

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

func TestMetadata(t *testing.T) {
	obj := &v1.Pod{}

	// Check nil annotations case
	assert.Equal(t, featureflag.Attributes{}, GetEnforcements(obj))

	// Check no metadata
	SetMetadata(obj, featureflag.Attributes{})
	if assert.Contains(t, obj.Labels, needsEnforceLabel) {
		assert.Equal(t, obj.Labels[needsEnforceLabel], falseVal)
	}
	assert.NotContains(t, obj.Annotations, enforcementsAnnotation)
	assert.False(t, IsEnforcementLabelSet(obj))
	assert.Equal(t, featureflag.Attributes{}, GetEnforcements(obj))

	// Check one enforcement
	SetMetadata(obj, featureflag.NewAttributes(map[string]string{"blah": trueVal}))
	if assert.Contains(t, obj.Labels, needsEnforceLabel) {
		assert.Equal(t, obj.Labels[needsEnforceLabel], trueVal)
	}
	if assert.Contains(t, obj.Annotations, enforcementsAnnotation) {
		assert.Equal(t, obj.Annotations[enforcementsAnnotation], "blah=true")
	}
	assert.True(t, IsEnforcementLabelSet(obj))
	assert.Equal(t, featureflag.NewAttributes(map[string]string{"blah": trueVal}), GetEnforcements(obj))

	// Check existing metadata and no enforcements
	obj.Labels = map[string]string{"fooLabel": "bar"}
	obj.Annotations = map[string]string{"fooAnno": "bar"}
	SetMetadata(obj, featureflag.Attributes{})
	if assert.Contains(t, obj.Labels, needsEnforceLabel) {
		assert.Equal(t, obj.Labels[needsEnforceLabel], falseVal)
	}
	assert.NotContains(t, obj.Annotations, enforcementsAnnotation)
	assert.False(t, IsEnforcementLabelSet(obj))
	assert.Equal(t, featureflag.Attributes{}, GetEnforcements(obj))
	assert.Equal(t, map[string]string{"fooLabel": "bar", needsEnforceLabel: falseVal}, obj.Labels)
	assert.Equal(t, map[string]string{"fooAnno": "bar"}, obj.Annotations)

	// Check existing metadata and multiple enforcements
	obj.Labels = map[string]string{"fooLabel": "bar"}
	obj.Annotations = map[string]string{"fooAnno": "bar"}
	SetMetadata(obj, featureflag.NewAttributes(map[string]string{"blah1": trueVal, "blah2": "fargo"}))
	if assert.Contains(t, obj.Labels, needsEnforceLabel) {
		assert.Equal(t, obj.Labels[needsEnforceLabel], trueVal)
	}
	if assert.Contains(t, obj.Annotations, enforcementsAnnotation) {
		assert.Equal(t, obj.Annotations[enforcementsAnnotation], "blah1=true,blah2=fargo")
	}
	assert.True(t, IsEnforcementLabelSet(obj))
	assert.Equal(t, featureflag.NewAttributes(map[string]string{"blah1": trueVal, "blah2": "fargo"}), GetEnforcements(obj))
	assert.Equal(t, map[string]string{"fooLabel": "bar", needsEnforceLabel: trueVal}, obj.Labels)
	assert.Equal(t, map[string]string{"fooAnno": "bar", enforcementsAnnotation: "blah1=true,blah2=fargo"},
		obj.Annotations)
}
