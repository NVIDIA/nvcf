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

package registry

import (
	"crypto/sha1" //nolint:gosec // UUID version 5 is defined over SHA-1; it is not used for security.
	"fmt"
	"regexp"
)

// poolIDNamespace is the UUID namespace of derived pool ids,
// 2f0c7a52-6b1e-4d3a-9c55-0b8e6f4a1d27.
var poolIDNamespace = [16]byte{
	0x2f, 0x0c, 0x7a, 0x52, 0x6b, 0x1e, 0x4d, 0x3a,
	0x9c, 0x55, 0x0b, 0x8e, 0x6f, 0x4a, 0x1d, 0x27,
}

// PoolID returns the pool id the server assigns to the pool named name in
// (clusterSet, cluster) when the writer names none: a name-based UUID
// (version 5) of "clusterSet/cluster/name". The id is the same on every
// replica, for a label-derived pool and for its API registration, and
// across re-registrations.
func PoolID(clusterSet, cluster, name string) string {
	h := sha1.New() //nolint:gosec // see the import.
	h.Write(poolIDNamespace[:])
	h.Write([]byte(clusterSet + "/" + cluster + "/" + name))
	var u [16]byte
	copy(u[:], h.Sum(nil))
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

var poolIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidPoolID reports whether a writer-supplied pool id is acceptable: 1 to
// 128 letters, digits, dots, dashes or underscores, starting with a letter or
// digit, so that it is safe in a REST path segment.
func ValidPoolID(id string) bool {
	return poolIDPattern.MatchString(id)
}
