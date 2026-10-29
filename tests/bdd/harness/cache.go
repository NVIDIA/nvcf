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

package harness

// CommandCache records the resolved command text of every successful
// run so a later "Given command has succeeded:" step in the same suite
// can short-circuit instead of re-executing. The key is the fully
// interpolated command string; two scenarios whose pre-${VAR} text is
// identical but whose env vars differ correctly miss the cache.
type CommandCache struct {
	succeeded map[string]struct{}
}

// NewCommandCache returns an empty cache.
func NewCommandCache() *CommandCache {
	return &CommandCache{succeeded: map[string]struct{}{}}
}

// Record marks resolvedCommand as having succeeded. Idempotent.
func (c *CommandCache) Record(resolvedCommand string) {
	c.succeeded[resolvedCommand] = struct{}{}
}

// Has reports whether resolvedCommand has been Record-ed in this suite.
func (c *CommandCache) Has(resolvedCommand string) bool {
	_, ok := c.succeeded[resolvedCommand]
	return ok
}
