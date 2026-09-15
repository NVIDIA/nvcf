/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package clusterdump

// Section names for the heavy bundle artifacts an operator selects via
// --include. The summary sections (nodes, GPU, ICMS, events, stuck namespaces)
// are always collected and are intentionally not represented here.
const (
	SectionResources = "resources"
	SectionLogs      = "logs"
	SectionHelm      = "helm"
	SectionAll       = "all"
)

// Redaction modes.
const (
	RedactSecrets = "secrets" // mask Secret values (default)
	RedactNone    = "none"    // no masking (trusted environments only)
	RedactAll     = "all"     // also scrub ConfigMaps
)

// DumpOptions controls the optional deep-capture probes and how aggressively
// their output is redacted. It is empty for the default lightweight stdout
// snapshot; setting any section turns on deep collection, which an operator
// runs to produce a diagnostic bundle to share with NVIDIA support.
type DumpOptions struct {
	Sections    map[string]bool // see Section* constants
	Redact      string          // see Redact* constants; "" behaves as RedactSecrets
	LogTail     int64           // tail lines per container (0 -> default)
	MaxLogBytes int             // hard ceiling per container log (0 -> default)
}

// Deep reports whether any deep-capture section is enabled.
func (o DumpOptions) Deep() bool { return len(o.Sections) > 0 }

// Has reports whether a section is enabled, directly or via "all".
func (o DumpOptions) Has(section string) bool {
	return o.Sections[SectionAll] || o.Sections[section]
}

// redactSecrets reports whether Secret values should be masked. Only the
// explicit "none" mode disables it; an unset mode defaults to masking.
func (o DumpOptions) redactSecrets() bool { return o.Redact != RedactNone }

// redactAll reports whether non-Secret config (e.g. ConfigMaps) should also be
// scrubbed.
func (o DumpOptions) redactAll() bool { return o.Redact == RedactAll }
