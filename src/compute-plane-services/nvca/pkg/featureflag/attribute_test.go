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

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAttributes(t *testing.T) {
	oldAttrs := attributes
	attributes = map[string]*Attribute{}
	t.Cleanup(func() {
		attributes = oldAttrs
	})
	for i := 0; i < 6; i++ {
		b := false
		newAttribute(fmt.Sprintf("foo%d", i+1), &b, "")
	}
	// enable foo7 by default
	b := true
	newAttribute("foo7", &b, "blah")
	parseAttributes("foo1=true,foo2,foo3=false,foo4=val")
	// skip foo5
	parseAttributes("foo6=true")
	// skip foo7
	parseAttributes("foo8=true")
	assert.True(t, attributes["foo1"].Enabled())
	assert.True(t, attributes["foo2"].Enabled())
	assert.False(t, attributes["foo3"].Enabled())
	assert.True(t, attributes["foo4"].Enabled())
	assert.False(t, attributes["foo5"].Enabled())
	assert.True(t, attributes["foo6"].Enabled())
	assert.True(t, attributes["foo7"].Enabled())
	assert.NotContains(t, attributes, "foo8")

	assert.Equal(t, NewAttributes(map[string]string{
		"foo1": "true",
		"foo2": "true",
		"foo4": "val",
		"foo6": "true",
		"foo7": "blah",
	}), GetEnabledAttributes())
	assert.Equal(t, "foo1=true,foo2=true,foo3=false,foo4=val,foo5=false,foo6=true,foo7=blah", AttrCLIFlag{}.String())
}
