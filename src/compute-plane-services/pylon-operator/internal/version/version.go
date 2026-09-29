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

// Package version holds build metadata. Bazel sets the variables through
// x_defs on the go_binary; plain go builds keep the defaults.
package version

var (
	// Service is the service name reported in logs.
	Service = "nvcf-pylon-operator"
	// Version is the release version.
	Version = "dev"
	// GitHash is the commit the binary was built from.
	GitHash = "unknown"
	// ReleaseTag is the image tag of a release build.
	ReleaseTag = ""
)

// KeysAndValues returns the build metadata as logr key-value pairs.
func KeysAndValues() []any {
	return []any{"service", Service, "version", Version, "gitHash", GitHash, "releaseTag", ReleaseTag}
}
