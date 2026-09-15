// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types

// Struct definitions will be added in Phase 1

// KubeletRequest is the structure of the JSON request from Kubelet.
type KubeletRequest struct {
	Image         string `json:"image"`
	CacheKeyType  string `json:"cacheKeyType,omitempty"`
	CacheDuration string `json:"cacheDuration,omitempty"`
}

// AuthConfig holds the username and password for a registry.
type AuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CredentialProviderResponse is the structure of the JSON response to Kubelet.
type CredentialProviderResponse struct {
	Kind          string                `json:"kind"`
	APIVersion    string                `json:"apiVersion"`
	CacheKeyType  string                `json:"cacheKeyType"`
	CacheDuration string                `json:"cacheDuration"`
	Auth          map[string]AuthConfig `json:"auth"`
}

// DockerConfigFile represents the structure of a Docker config.json file.
type DockerConfigFile struct {
	Auths map[string]DockerAuthEntry `json:"auths"`
	// Potentially other fields like HttpHeaders, CredsStore could be added if needed.
}

// DockerAuthEntry represents a single auth entry in the Docker config.json.
type DockerAuthEntry struct {
	Auth  string `json:"auth"`
	Email string `json:"email,omitempty"` // Email is often present but not always used by us.
}
