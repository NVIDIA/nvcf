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

package consts

// EnvVaultAllowPendingRemovalMounts allows Pending Removal builtins to be
// mounted as if they are Deprecated to facilitate migration to supported
// builtin plugins.
const EnvVaultAllowPendingRemovalMounts = "VAULT_ALLOW_PENDING_REMOVAL_MOUNTS"

// DeprecationStatus represents the current deprecation state for builtins
type DeprecationStatus uint32

// These are the states of deprecation for builtin plugins
const (
	Supported = iota
	Deprecated
	PendingRemoval
	Removed
	Unknown
)

// String returns the string representation of a builtin deprecation status
func (s DeprecationStatus) String() string {
	switch s {
	case Supported:
		return "supported"
	case Deprecated:
		return "deprecated"
	case PendingRemoval:
		return "pending removal"
	case Removed:
		return "removed"
	default:
		return ""
	}
}
