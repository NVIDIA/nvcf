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

package dependency

import "os"

type NvVaultQuery struct {
	// errorOnMissingKeyEnabled determines if query is linked to a template that has error_on_missing_key enabled
	errorOnMissingKeyEnabled bool

	// runOnce determines if the query should be run only once
	runOnce bool

	// skipMountVersionCheck determines if the secret path(s) defined in the template
	// should be validated via sys/internal/ui/mounts before fetching secrets.
	skipMountVersionCheck bool

	// exitOnClientError determines if process should exit if the secret path(s) defined in the template
	// return a 40x status code
	exitOnClientError bool

	// stopProcessingOnClientError determines if template processing should stop (but keep agent running)
	// if the secret path(s) defined in the template return a 40x status code
	stopProcessingOnClientError bool

	// destination is the configured templated destination
	destination string

	// templateID associated with the dependency
	templateID string
}

// added for nv
// fileExists checks for presence of passed file path
func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return !os.IsNotExist(err)
}
