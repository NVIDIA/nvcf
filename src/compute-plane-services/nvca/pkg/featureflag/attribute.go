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
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/urfave/cli/v2"
)

var (
	attributes = map[string]*Attribute{}
	attrMu     sync.Mutex
)

var (
	// Both AttrKataRuntimeIsolation and AttrTimeSlicingGPUEnabled modify the GPU resource subscription key of pods
	// when using `nvidia.com/gpu` as the GPU resource key.
	// However, if both attributes are present, AttrKataRuntimeIsolation takes precedence.
	AttrKataRuntimeIsolation = newAttribute("KataRuntimeIsolation", newBool(false), "")
	// HostIsolation enables function/task level tenant boundary (cannot be mingled with AccountIsolation)
	// HostIsolation can be co-mingled with BinPackTenantWorkloads
	AttrHostIsolation = newAttribute("HostIsolation", newBool(false), "")
	// AccountIsolation enables only NCAId level tenant boundary (cannot be mingled with AttrHostIsolation)
	// AccountIsolation can be co-mingled with BinPackTenantWorkloads
	AttrAccountIsolation      = newAttribute("AccountIsolation", newBool(false), "")
	AttrTimeSlicingGPUEnabled = newAttribute("TimeSlicingGPUEnabled", newBool(false), "")
	AttrPassthroughGPUEnabled = newAttribute("PassthroughGPUEnabled", newBool(false), "")
	// Turns on all OVC/SensorRTX risk mitigations
	AttrOVCSecurityEnforcements = newAttribute("OVCSecurityEnforcements", newBool(false), "")
	AttrNVLinkOptimized         = newAttribute("NVLinkOptimized", newBool(false), "")
)

var _ cli.Generic = AttrCLIFlag{}

type AttrCLIFlag struct{}

func (s AttrCLIFlag) Set(value string) error {
	parseAttributes(value)
	return nil
}

const trueVal = "true"

func (s AttrCLIFlag) String() string {
	attrMu.Lock()
	defer attrMu.Unlock()

	keys := make([]string, len(attributes))
	i := 0
	for _, f := range attributes {
		keys[i] = f.Key
		i++
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		f := attributes[k]
		sb.WriteString(f.Key)
		if f.Enabled() {
			val := f.val
			if val == "" {
				val = trueVal
			}
			sb.WriteString("=" + val)
		} else {
			sb.WriteString("=false")
		}
		if i != len(keys)-1 {
			sb.WriteByte(',')
		}
	}

	return sb.String()
}

func GetEnabledAttributes() (out Attributes) {
	attrMu.Lock()
	defer attrMu.Unlock()

	out = Attributes{
		m: map[string]string{},
	}
	for _, attr := range attributes {
		if attr.Enabled() {
			val := attr.val
			if val == "" {
				val = trueVal
			}
			out.m[attr.Key] = val
		}
	}
	return out
}

type Attributes struct {
	m map[string]string
}

func NewAttributes(m map[string]string) Attributes {
	return Attributes{m: m}
}

func (a Attributes) Iter() func(yield func(k, v string) bool) {
	return func(yield func(k, v string) bool) {
		for mk, mv := range a.m {
			if !yield(mk, mv) {
				return
			}
		}
	}
}

func (a Attributes) Len() int { return len(a.m) }

func (a Attributes) Empty() bool { return a.Len() == 0 }

func (a Attributes) Enabled(ff *Attribute) bool {
	return a.m != nil && a.m[ff.Key] == trueVal
}

// newAttribute creates an attribute
func newAttribute(key string, defaultEnabled *bool, val string) *Attribute {
	attrMu.Lock()
	defer attrMu.Unlock()

	f := attributes[key]
	if f == nil {
		f = &Attribute{
			Key: key,
			val: val,
		}
		attributes[key] = f
	}

	if f.defaultEnabled == nil {
		f.defaultEnabled = defaultEnabled
	}

	return f
}

// Attribute defines a attribute
type Attribute struct {
	Key            string
	enabled        *bool
	defaultEnabled *bool
	val            string
}

// Enabled checks if the attribute is enabled
func (f *Attribute) Enabled() bool {
	if f.enabled != nil {
		return *f.enabled
	}
	if f.defaultEnabled != nil {
		return *f.defaultEnabled
	}
	return false
}

// parseAttributes responsible for parse out the attribute usage
func parseAttributes(f string) {
	attrMu.Lock()
	defer attrMu.Unlock()

	log := core.GetLogger(core.WithDefaultLogger(context.Background()))

	f = strings.TrimSpace(f)

	for _, s := range strings.Split(f, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}

		enabled := true
		vs := strings.Split(s, "=")
		var val string
		switch len(vs) {
		case 1:
			s = vs[0]
			val = trueVal
		case 2:
			s = vs[0]
			val = vs[1]
			if b, err := strconv.ParseBool(val); err == nil {
				enabled = b
			} else {
				enabled = true
			}
		default:
			log.Warnf("Bad Attribute value %q", s)
			continue
		}

		if ff := attributes[s]; ff != nil {
			enabledStr := "disabled"
			if enabled {
				enabledStr = "enabled"
			}
			log.Infof("Attribute %s: %q=%s", enabledStr, ff.Key, val)
			ff.enabled = &enabled
			ff.val = val
		} else {
			log.Warnf("Unknown Attribute %q", s)
		}
	}
}
