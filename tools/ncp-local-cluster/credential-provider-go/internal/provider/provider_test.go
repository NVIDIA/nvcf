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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/tools/ncp-local-cluster/credential-provider-go/internal/types"
)

// Tests will be added in Phase 2

// Helper to create a temporary docker config file for testing
func createTempDockerConfig(t *testing.T, content string) string {
	t.Helper()
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "config.json")
	err := os.WriteFile(filePath, []byte(content), 0600)
	if err != nil {
		t.Fatalf("Failed to create temp docker config: %v", err)
	}
	return filePath
}

// Helper to base64 encode username:password string
func basicAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

func TestHandleGetCredentials(t *testing.T) {
	// --- Mock Docker Config Contents ---
	validEmptyDockerConfig := `{}`

	dockerConfigMulti := fmt.Sprintf(`{
		"auths": {
			"nvcr.io/ngc-org/ngc-team/repository": { "auth": "%s" },
			"nvcr.io/org/project": { "auth": "%s" },
			"nvcr.io": { "auth": "%s" }, 
			"index.docker.io/library/ubuntu": { "auth": "%s" }, 
			"docker.io/myuser/myimage": { "auth": "%s" }, 
			"docker.io": { "auth": "%s" }, 
			"https://quay.io": { "auth": "%s" }, 
			"localhost:5000/localimage": { "auth": "%s" }
		}
	}`, basicAuth("repouser", "repopass"),
		basicAuth("orguser", "orgpass"),
		basicAuth("nvcrdefault", "nvcrdefaultpass"),
		basicAuth("ubuntuuser", "ubuntupass"),           // For index.docker.io/library/ubuntu
		basicAuth("myuser", "myuserpass"),               // For docker.io/myuser/myimage
		basicAuth("dockerdefault", "dockerdefaultpass"), // For docker.io fallback
		basicAuth("quayuser", "quaypass"),
		basicAuth("localuser", "localpass"))

	dockerConfigMultiPath := fmt.Sprintf(`{
		"auths": {
			"nvcr.io/long/path/specific": { "auth": "%s" },
			"nvcr.io/long/path": { "auth": "%s" },
			"nvcr.io/long": { "auth": "%s" },
			"nvcr.io": { "auth": "%s" },
			"index.docker.io/userone/repoone/imageone": { "auth": "%s" },
			"index.docker.io/userone/repoone": { "auth": "%s" },
			"docker.io/usertwo/repotwo": { "auth": "%s" },
			"index.docker.io/userthree": { "auth": "%s" },
			"docker.io": { "auth": "%s" }
		}
	}`, basicAuth("longpathspecificuser", "longpathspecificpass"),
		basicAuth("longpathuser", "longpathpass"),
		basicAuth("longuser", "longpass"),
		basicAuth("nvcrdefault", "nvcrdefaultpass"), // Reusing from dockerConfigMulti for consistency
		basicAuth("idxuseroneimagerepone", "idxuseroneimagereponepass"),
		basicAuth("idxuseronerepoone", "idxuseronerepoonepass"),
		basicAuth("docusertworepotwo", "docusertworepotwopass"),
		basicAuth("idxuserthree", "idxuserthreepass"),
		basicAuth("dockerdefault", "dockerdefaultpass")) // Reusing from dockerConfigMulti

	dockerConfigMalformedBase64 := fmt.Sprintf(`{
		"auths": {
			"nvcr.io": { "auth": "not@val!d=base64" }
		}
	}`)

	dockerConfigAuthNoColon := fmt.Sprintf(`{
		"auths": {
			"nvcr.io": { "auth": "%s" }
		}
	}`, base64.StdEncoding.EncodeToString([]byte("justuser")))

	// --- Expected Responses ---
	createExpectedResponse := func(host, user, pass, cacheKeyType, cacheDuration string) *types.CredentialProviderResponse {
		return &types.CredentialProviderResponse{
			Kind:          responseKind,
			APIVersion:    apiVersion,
			CacheKeyType:  cacheKeyType,
			CacheDuration: cacheDuration,
			Auth:          map[string]types.AuthConfig{host: {Username: user, Password: pass}},
		}
	}

	expectedEmptyAuthResponse := &types.CredentialProviderResponse{
		Kind:          responseKind,
		APIVersion:    apiVersion,
		CacheKeyType:  defaultCacheKeyType,
		CacheDuration: defaultCacheDuration,
		Auth:          map[string]types.AuthConfig{},
	}

	tests := []struct {
		name             string
		requestBody      []byte
		dockerConfigJSON string // Use direct JSON string for clarity
		wantResponse     *types.CredentialProviderResponse
		wantErrMsg       string
	}{
		// --- Happy Path: Specific path matches ---
		{
			name:             "nvcr.io path-specific repository",
			requestBody:      []byte(`{"image": "nvcr.io/ngc-org/ngc-team/repository:latest"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("nvcr.io", "repouser", "repopass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "nvcr.io path-specific org/project",
			requestBody:      []byte(`{"image": "nvcr.io/org/project"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("nvcr.io", "orguser", "orgpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "index.docker.io path-specific library/ubuntu",
			requestBody:      []byte(`{"image": "ubuntu"}`), // Will be parsed to index.docker.io/library/ubuntu
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("docker.io", "ubuntuuser", "ubuntupass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "docker.io path-specific myuser/myimage",
			requestBody:      []byte(`{"image": "myuser/myimage:v1"}`), // Will be parsed to index.docker.io/myuser/myimage, then check docker.io/myuser/myimage
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("docker.io", "myuser", "myuserpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "localhost with port and path",
			requestBody:      []byte(`{"image": "localhost:5000/localimage"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("localhost:5000", "localuser", "localpass", defaultCacheKeyType, defaultCacheDuration),
		},

		// --- Happy Path: Fallback to host ---
		{
			name:             "nvcr.io fallback (path not found)",
			requestBody:      []byte(`{"image": "nvcr.io/unknown/path:tag"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("nvcr.io", "nvcrdefault", "nvcrdefaultpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "docker.io fallback (path not found for official image)",
			requestBody:      []byte(`{"image": "nonexistentlibrary/image"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("docker.io", "dockerdefault", "dockerdefaultpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "quay.io fallback (https prefix in config)",
			requestBody:      []byte(`{"image": "quay.io/namespace/image:tag"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("quay.io", "quayuser", "quaypass", defaultCacheKeyType, defaultCacheDuration),
		},

		// --- Happy Path: Longest Prefix Path Matching (New Tests) ---
		{
			name:             "nvcr.io match most specific path",
			requestBody:      []byte(`{"image": "nvcr.io/long/path/specific/myimage:latest"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("nvcr.io", "longpathspecificuser", "longpathspecificpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "nvcr.io match middle path",
			requestBody:      []byte(`{"image": "nvcr.io/long/path/anotherimage:v2"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("nvcr.io", "longpathuser", "longpathpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "nvcr.io match shortest path",
			requestBody:      []byte(`{"image": "nvcr.io/long/other/image:tag"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("nvcr.io", "longuser", "longpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "nvcr.io fallback to host after path mismatch",
			requestBody:      []byte(`{"image": "nvcr.io/newroot/image:tag"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("nvcr.io", "nvcrdefault", "nvcrdefaultpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "index.docker.io match most specific path", // index.docker.io/userone/repoone/imageone
			requestBody:      []byte(`{"image": "index.docker.io/userone/repoone/imageone/final:tag"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("docker.io", "idxuseroneimagerepone", "idxuseroneimagereponepass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "index.docker.io match middle path", // index.docker.io/userone/repoone
			requestBody:      []byte(`{"image": "index.docker.io/userone/repoone/another:tag"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("docker.io", "idxuseronerepoone", "idxuseronerepoonepass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name: "index.docker.io match path aliased to docker.io key", // docker.io/usertwo/repotwo
			// Image is index.docker.io/usertwo/repotwo/app:v1 -> specificLookupKey will be index.docker.io/usertwo/repotwo
			// Iterative path matching for index.docker.io will not find index.docker.io/usertwo/repotwo
			// Then, the aliased iterative path matching for docker.io will find docker.io/usertwo/repotwo
			requestBody:      []byte(`{"image": "index.docker.io/usertwo/repotwo/app:v1"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("docker.io", "docusertworepotwo", "docusertworepotwopass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "index.docker.io match shortest path (userthree)", // index.docker.io/userthree
			requestBody:      []byte(`{"image": "index.docker.io/userthree/someapp/beta:tag"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("docker.io", "idxuserthree", "idxuserthreepass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "docker.io fallback after path mismatch (new config)",
			requestBody:      []byte(`{"image": "docker.io/unknownuser/unknownrepo/image"}`),
			dockerConfigJSON: dockerConfigMultiPath,
			wantResponse:     createExpectedResponse("docker.io", "dockerdefault", "dockerdefaultpass", defaultCacheKeyType, defaultCacheDuration),
		},
		{
			name:             "image directly under docker.io matching docker.io default",
			requestBody:      []byte(`{"image": "docker.io/myimage"}`), // This means index.docker.io/library/myimage
			dockerConfigJSON: dockerConfigMultiPath,                    // Should fallback to docker.io creds
			wantResponse:     createExpectedResponse("docker.io", "dockerdefault", "dockerdefaultpass", defaultCacheKeyType, defaultCacheDuration),
		},

		// --- Happy Path: Custom cache settings from request ---
		{
			name:             "nvcr.io with custom cache settings",
			requestBody:      []byte(`{"image": "nvcr.io/ngc-org/ngc-team/repository", "cacheKeyType": "Image", "cacheDuration": "15m"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     createExpectedResponse("nvcr.io", "repouser", "repopass", "Image", "15m"),
		},

		// --- Edge Cases & Error Conditions ---
		{
			name:             "missing image in request",
			requestBody:      []byte(`{}`),
			dockerConfigJSON: validEmptyDockerConfig,
			wantErrMsg:       "image field is required in the request",
		},
		{
			name:             "malformed input JSON",
			requestBody:      []byte(`{"image": "nvcr.io/ngc-org/ngc-team/repository"`), // Missing closing brace
			dockerConfigJSON: validEmptyDockerConfig,
			wantErrMsg:       "failed to unmarshal request body",
		},
		{
			name:             "non-existent docker config file",
			requestBody:      []byte(`{"image": "nvcr.io/ngc-org/ngc-team/repository"}`),
			dockerConfigJSON: "non-existent-file.json", // Special value to indicate file should not be created
			wantErrMsg:       "failed to read docker config file",
		},
		{
			name:             "malformed docker config JSON",
			requestBody:      []byte(`{"image": "nvcr.io/ngc-org/ngc-team/repository"}`),
			dockerConfigJSON: `{"auths":malformed`, // Malformed JSON
			wantErrMsg:       "failed to unmarshal docker config file",
		},
		{
			name:             "no credentials found for any matching key",
			requestBody:      []byte(`{"image": "completelyunknown.com/user/repo:tag"}`),
			dockerConfigJSON: dockerConfigMulti,
			wantResponse:     expectedEmptyAuthResponse,
		},
		{
			name:             "docker config with malformed base64 auth token",
			requestBody:      []byte(`{"image": "nvcr.io/nvidia/cuda"}`),
			dockerConfigJSON: dockerConfigMalformedBase64,
			wantErrMsg:       "failed to decode auth token for nvcr.io", // Error refers to hostLookupKey
		},
		{
			name:             "docker config auth token without colon",
			requestBody:      []byte(`{"image": "nvcr.io/nvidia/cuda"}`),
			dockerConfigJSON: dockerConfigAuthNoColon,
			wantErrMsg:       "decoded auth token for nvcr.io (matched key used for image nvcr.io/nvidia/cuda) is malformed",
		},
		{
			name:             "image name that fails parsing (e.g. invalid char)",
			requestBody:      []byte(fmt.Sprintf(`{"image": "%s"}`, string([]byte{0x7f}))), // DEL char, invalid in ref
			dockerConfigJSON: validEmptyDockerConfig,
			wantErrMsg:       "failed to parse image name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var configPath string
			if tt.dockerConfigJSON == "non-existent-file.json" {
				configPath = filepath.Join(t.TempDir(), "non_existent_config.json")
				// Do not create the file
			} else {
				configPath = createTempDockerConfig(t, tt.dockerConfigJSON)
			}

			gotResponse, err := HandleGetCredentials(tt.requestBody, configPath)

			if tt.wantErrMsg != "" {
				if err == nil {
					t.Errorf("HandleGetCredentials() error = nil, wantErr substring %q", tt.wantErrMsg)
					return
				}
				if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("HandleGetCredentials() error = %q, wantErr substring %q", err, tt.wantErrMsg)
				}
				if gotResponse != nil {
					t.Errorf("HandleGetCredentials() gotResponse = %v, want nil when error is expected", gotResponse)
				}
				return
			}

			if err != nil {
				t.Errorf("HandleGetCredentials() unexpected error = %v", err)
				return
			}

			if !reflect.DeepEqual(gotResponse, tt.wantResponse) {
				t.Errorf("HandleGetCredentials() gotResponse = %+v, want %+v", gotResponse, tt.wantResponse)
			}
		})
	}
}
