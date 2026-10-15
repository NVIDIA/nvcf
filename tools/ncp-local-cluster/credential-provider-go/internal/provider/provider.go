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

package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/tools/ncp-local-cluster/credential-provider-go/internal/types"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	defaultCacheKeyType  = "Registry"
	defaultCacheDuration = "5m" // As per current test definitions. Original bash script used 10m.
	apiVersion           = "credentialprovider.kubelet.k8s.io/v1"
	responseKind         = "CredentialProviderResponse"
	// dockerHubRegistry is defined by the name library as "index.docker.io"
	// For consistency with how users might write it in config.json keys, we might still use "docker.io"
	// Let's see how the library normalizes and how it affects lookups.
)

// parseImageNameForRegistryKeys extracts the specific key for path-based matching
// and a host-only key for fallback matching, using go-containerregistry/pkg/name.
func parseImageNameForRegistryKeys(imageName string) (specificLookupKey, hostLookupKey string, err error) {
	if imageName == "" {
		return "", "", errors.New("image name cannot be empty")
	}

	ref, err := name.ParseReference(imageName, name.WeakValidation) // WeakValidation is often more forgiving for various inputs
	if err != nil {
		return "", "", fmt.Errorf("failed to parse image reference '%s': %w", imageName, err)
	}

	// specificLookupKey is the full repository name (e.g., index.docker.io/library/ubuntu, nvcr.io/nvidia/cuda)
	// ref.Context().Name() gives registry/repository (without tag/digest)
	specificLookupKey = ref.Context().Name()

	// hostLookupKey is just the registry/host part (e.g., index.docker.io, nvcr.io)
	hostLookupKey = ref.Context().RegistryStr()

	// The go-containerregistry library normalizes Docker Hub to "index.docker.io".
	// Users often write "docker.io" in configs. If hostLookupKey is "index.docker.io",
	// we might want to ensure our fallback logic also considers "docker.io" or that
	// specificLookupKey also reflects "docker.io" if that's how keys are stored.
	// For now, let's use the direct output. Tests will reveal if aliasing is needed.
	// If specificLookupKey starts with "index.docker.io/", it's common to also check for keys starting with "docker.io/"
	// However, ADR002 schema shows keys like "nvcr.io/ngc-org/ngc-team/repository", not index.docker.io, so direct output is probably best.

	return specificLookupKey, hostLookupKey, nil
}

// HandleGetCredentials processes the Kubelet request and returns credentials.
func HandleGetCredentials(requestBody []byte, dockerConfigPath string) (*types.CredentialProviderResponse, error) {
	var req types.KubeletRequest
	err := json.Unmarshal(requestBody, &req)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
	}

	if req.Image == "" {
		return nil, errors.New("image field is required in the request")
	}

	specificLookupKey, hostLookupKey, err := parseImageNameForRegistryKeys(req.Image)
	if err != nil {
		// If parsing fails, it's hard to know what registry to even try for.
		// The Kubelet spec implies for some errors provider should return empty JSON and exit 0.
		// For now, returning an error as per current test expectations.
		return nil, fmt.Errorf("failed to parse image name '%s' for registry key extraction: %w", req.Image, err)
	}

	// Determine CacheKeyType and CacheDuration
	cacheKeyType := req.CacheKeyType
	if cacheKeyType == "" || cacheKeyType == "null" { // "null" string check as per bash script
		cacheKeyType = defaultCacheKeyType
	}
	cacheDuration := req.CacheDuration
	if cacheDuration == "" || cacheDuration == "null" { // "null" string check as per bash script
		cacheDuration = defaultCacheDuration
	}

	dockerConfigData, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read docker config file %s: %w", dockerConfigPath, err)
	}

	var dockerConf types.DockerConfigFile
	err = json.Unmarshal(dockerConfigData, &dockerConf)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal docker config file %s: %w", dockerConfigPath, err)
	}

	var authEntry types.DockerAuthEntry
	var found bool
	var matchedKey string // Store the key that successfully matched

	// Lookup attempts:
	// 1. Iteratively try specific path keys, from most specific to least specific repository path parts.
	//    e.g., for "nvcr.io/org/project/image", try:
	//    - "nvcr.io/org/project/image"
	//    - "nvcr.io/org/project"
	//    - "nvcr.io/org" (though usually Docker config stops at registry/project or just registry)
	//    The loop below will stop when currentLookupKey becomes just the host.
	currentLookupKey := specificLookupKey
	for currentLookupKey != hostLookupKey && currentLookupKey != "" {
		authEntry, found = dockerConf.Auths[currentLookupKey]
		if found {
			matchedKey = currentLookupKey
			break
		}
		lastSlash := strings.LastIndex(currentLookupKey, "/")
		if lastSlash == -1 { // Should not happen if currentLookupKey starts with host/
			break
		}
		// Stop if the next key would be just the host, to avoid duplicate check with hostLookupKey later
		// or if it becomes empty or invalid.
		potentialNextKey := currentLookupKey[:lastSlash]
		if potentialNextKey == hostLookupKey || !strings.Contains(potentialNextKey, "/") {
			// If stripping the last segment results in just the host, or something before the host (e.g. "nvcr.io" -> ""), stop.
			// The hostLookupKey will be tried separately.
			break
		}
		currentLookupKey = potentialNextKey
	}

	// 2. If not found by iterative path matching, and if the original specific key was for Docker Hub (index.docker.io),
	//    try the same iterative logic with "docker.io/" prefix.
	if !found && strings.HasPrefix(specificLookupKey, "index.docker.io/") {
		dockerIoEquivalentSpecificKey := "docker.io/" + strings.TrimPrefix(specificLookupKey, "index.docker.io/")
		currentDockerIoLookupKey := dockerIoEquivalentSpecificKey
		for currentDockerIoLookupKey != "docker.io" && currentDockerIoLookupKey != "" { // Stop at "docker.io"
			authEntry, found = dockerConf.Auths[currentDockerIoLookupKey]
			if found {
				matchedKey = currentDockerIoLookupKey
				break
			}
			lastSlash := strings.LastIndex(currentDockerIoLookupKey, "/")
			if lastSlash == -1 {
				break
			}
			potentialNextKey := currentDockerIoLookupKey[:lastSlash]
			if potentialNextKey == "docker.io" || !strings.Contains(potentialNextKey, "/") {
				break
			}
			currentDockerIoLookupKey = potentialNextKey
		}
	}

	// 3. If still not found, try plain host key (e.g., "nvcr.io", "index.docker.io")
	if !found {
		authEntry, found = dockerConf.Auths[hostLookupKey]
		if found {
			matchedKey = hostLookupKey
		}
	}

	// 4. If not found and host key was Docker Hub (index.docker.io), try with "docker.io"
	if !found && hostLookupKey == "index.docker.io" {
		authEntry, found = dockerConf.Auths["docker.io"]
		if found {
			matchedKey = "docker.io"
		}
	}

	// 5. If still not found, try https:// + host key (e.g., "https://nvcr.io", "https://index.docker.io")
	if !found {
		httpsHostKey := "https://" + hostLookupKey
		authEntry, found = dockerConf.Auths[httpsHostKey]
		if found {
			matchedKey = httpsHostKey
		}
	}

	// 6. If not found, host key was Docker Hub (index.docker.io), try with "https://docker.io"
	if !found && hostLookupKey == "index.docker.io" {
		httpsDockerIoKey := "https://docker.io"
		authEntry, found = dockerConf.Auths[httpsDockerIoKey]
		if found {
			matchedKey = httpsDockerIoKey
		}
	}

	if !found {
		// No credentials found, return empty auth map
		return &types.CredentialProviderResponse{
			Kind:          responseKind,
			APIVersion:    apiVersion,
			CacheKeyType:  cacheKeyType,
			CacheDuration: cacheDuration,
			Auth:          map[string]types.AuthConfig{},
		}, nil
	}

	decodedAuth, err := base64.StdEncoding.DecodeString(authEntry.Auth)
	if err != nil {
		// Use matchedKey if available for better error context, otherwise fallback
		errorContextKey := hostLookupKey // Default to hostLookupKey
		if matchedKey != "" {
			errorContextKey = matchedKey
		} else if specificLookupKey != "" { // Fallback to specificLookupKey if matchedKey is empty
			errorContextKey = specificLookupKey
		}
		return nil, fmt.Errorf("failed to decode auth token for %s (matched key used for image %s, auth value: %s): %w", errorContextKey, req.Image, authEntry.Auth, err)
	}

	parts := strings.SplitN(string(decodedAuth), ":", 2)
	if len(parts) != 2 {
		// Use matchedKey if available for better error context
		errorContextKey := hostLookupKey // Default to hostLookupKey
		if matchedKey != "" {
			errorContextKey = matchedKey
		} else if specificLookupKey != "" {
			errorContextKey = specificLookupKey
		}
		return nil, fmt.Errorf("decoded auth token for %s (matched key used for image %s) is malformed (expected user:pass): %s", errorContextKey, req.Image, string(decodedAuth))
	}
	username := parts[0]
	password := parts[1]

	// The key in the response Auth map should be the one Kubelet expects for the registry.
	// This is typically the plain host (e.g., "nvcr.io", or "docker.io" for Docker Hub).
	responseHostKey := hostLookupKey
	if hostLookupKey == "index.docker.io" {
		responseHostKey = "docker.io" // Normalize Docker Hub for the response key
	}

	response := &types.CredentialProviderResponse{
		Kind:          responseKind,
		APIVersion:    apiVersion,
		CacheKeyType:  cacheKeyType,
		CacheDuration: cacheDuration,
		Auth: map[string]types.AuthConfig{
			responseHostKey: {
				Username: username,
				Password: password,
			},
		},
	}

	return response, nil
}
