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

package reval

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/image-credential-helper/credhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	orasregistry "oras.land/oras-go/v2/registry"
)

var (
	//go:embed testdata/test-chart/exp_pass_basic.json
	expPassBasic string
	//go:embed testdata/test-chart/exp_pass_extras.json
	expPassExtras string
	//go:embed testdata/test-chart-multi-service/exp.json
	expPassMultiService string
	//go:embed testdata/test-chart-extra-gvks-only/exp.json
	expPassExtraGVKsOnly string
)

type fakeImageChecker struct {
	badImageTags   []string
	legacyImageReg string
	publicImageReg string
}

func (f fakeImageChecker) checkImageExists(ctx context.Context, ref orasregistry.Reference, imgAuthCfg common.RegistryAuthConfig, legacyAPIKey string) (imageAuthType, int, error) {
	imageTag := ref.String()
	for _, badImageTag := range f.badImageTags {
		if badImageTag == imageTag {
			return 0, -1, fmt.Errorf("unauthorized on image: %q", imageTag)
		}
	}
	ref, err := orasregistry.ParseReference(imageTag)
	if err != nil {
		return 0, -1, err
	}
	regHost := ref.Registry
	if f.publicImageReg != "" && regHost == f.publicImageReg {
		return public, -1, nil
	}
	if f.legacyImageReg != "" && regHost == f.legacyImageReg {
		return legacy, -1, nil
	}
	for i, secret := range imgAuthCfg.K8sSecrets {
		if _, ok := secret.Auths[regHost]; ok {
			return thirdParty, i, nil
		}
	}
	return 0, -1, fmt.Errorf("no secret found for image: %q", imageTag)
}

// fakeEnvAuthHelper returns fixed credentials when LoadFromEnv is set,
// allowing tests to exercise the environment-based credential path
// without real CSP configuration.
type fakeEnvAuthHelper struct {
	host     string
	username string
	password string
}

func (f fakeEnvAuthHelper) Matches(serverURL *url.URL) (bool, bool) {
	return serverURL.Host == f.host, false
}

func (f fakeEnvAuthHelper) Run(_ context.Context, _ *url.URL, creds credhelper.AuthHelperCredentials) (string, string, error) {
	if creds.LoadFromEnv {
		return f.username, f.password, nil
	}
	return creds.Username, creds.Password, nil
}

type noopAuthHelper struct{}

func (noopAuthHelper) Matches(*url.URL) (bool, bool) { return false, false }
func (noopAuthHelper) Run(context.Context, *url.URL, credhelper.AuthHelperCredentials) (string, string, error) {
	return "", "", nil
}

func TestRun(t *testing.T) {
	logger := zaptest.NewLogger(t)
	testdataDir := "testdata"

	origPlain := plainHTTP
	plainHTTP = true
	t.Cleanup(func() { plainHTTP = origPlain })

	h, err := NewHandler(logger, "reval", HandlerOptions{})
	require.NoError(t, err)
	h.newImageChecker = func() imageChecker { return fakeImageChecker{} }

	password := "blah"
	basicAuth := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "$oauthtoken:%s", password))
	srv := newTestHelmRepoServer(t, testdataDir, password)
	t.Cleanup(srv.Close)
	srvURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	srvHost := srvURL.Host

	t.Run("pass basic", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			Values: json.RawMessage(`{
				"podLabels": {
					"shouldnotexist": "val"
				},
				"resources": {
					"limits": {
						"nvidia.com/gpu": 1
					}
				},
				"depVolumes": [{
					"name": "emptydirvol",
					"emptyDir": {
					    "sizeLimit": "500Mi"
					}
				}],
				"depVolumeMounts": [{
					"name": "emptydirvol",
					"mountPath": "/var/foo"
				}]
			}`),
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassBasic, gotStr)
		}
	})

	t.Run("pass multi service", func(t *testing.T) {
		h, err := NewHandler(logger, "reval", HandlerOptions{
			PreserveLabels: []string{
				podIndexLabel,
			},
			PreserveAnnotations: []string{
				"preserved-annotation",
			},
		})
		require.NoError(t, err)
		h.newImageChecker = func() imageChecker { return fakeImageChecker{} }

		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart-multi-service", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{
					"nvcr.io": {Auth: basicAuth},
					"ghcr.io": {Auth: basicAuth},
				}}},
			},
			Namespace:         "sr-1234",
			ReleaseName:       "mini-service",
			TargetServiceName: "mychart-service",
			TargetServicePort: 8000,
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassMultiService, gotStr)
		}
	})

	t.Run("pass extras", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
			APIVersions: []string{"monitoring.coreos.com/v1beta1"},
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassExtras, gotStr)
		}
	})

	t.Run("pass render extra GVKs only", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart-extra-gvks-only", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
			RenderPolicy: ValidationPolicy{
				Name: DefaultPolicy,
				ExtraGVKs: []schema.GroupVersionKind{{
					Group:   "nvidia.com",
					Version: "v1alpha1",
					Kind:    "DynamoGraphDeployment",
				}},
			},
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassExtraGVKsOnly, gotStr)
		}
	})

	t.Run("pass validate extra GVKs only", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   false,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart-extra-gvks-only", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
			ValidatePolicies: []ValidationPolicy{
				{
					Name: DefaultPolicy,
					ExtraGVKs: []schema.GroupVersionKind{{
						Group:   "nvidia.com",
						Version: "v1alpha1",
						Kind:    "DynamoGraphDeployment",
					}},
				},
			},
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		assert.True(t, result.Valid)
	})

	t.Run("pass extras validate", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		cfg := Config{
			Render:   false,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
			APIVersions: []string{"monitoring.coreos.com/v1beta1"},
		}

		result, err := h.Run(context.Background(), cfg, nil)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		assert.True(t, result.Valid)
	})

	t.Run("pass health", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:                "sr-1234",
			ReleaseName:              "mini-service",
			K8sVersion:               "1.30.0",
			APIVersions:              []string{"monitoring.coreos.com/v1beta1"},
			TargetServiceName:        "mini-service-test-chart-pod-sel",
			TargetServicePort:        80,
			TargetHTTPHealthEndpoint: "/",
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassExtras, gotStr)
		}
	})

	t.Run("pass multi auth", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{
					{
						Auths: map[string]common.RegistryAuth{
							srvHost:     {Auth: basicAuth},
							"other.com": {Auth: basicAuth},
						},
					},
					{
						Auths: map[string]common.RegistryAuth{
							srvHost:      {Auth: base64.StdEncoding.EncodeToString([]byte("foo:bar"))},
							"other2.com": {Auth: basicAuth},
						},
					},
				},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			Values: json.RawMessage(`{
				"podLabels": {
					"shouldnotexist": "val"
				},
				"resources": {
					"limits": {
						"nvidia.com/gpu": 1
					}
				},
				"depVolumes": [{
					"name": "emptydirvol",
					"emptyDir": {
					    "sizeLimit": "500Mi"
					}
				}],
				"depVolumeMounts": [{
					"name": "emptydirvol",
					"mountPath": "/var/foo"
				}]
			}`),
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassBasic, gotStr)
		}
	})

	t.Run("pass legacy auth", func(t *testing.T) {
		ngcHostReTmp := ngcHostRe
		ngcHostRe = regexp.MustCompile(".*")
		t.Cleanup(func() { ngcHostRe = ngcHostReTmp })

		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:      true,
			ChartURL:    fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			NGCAPIKey:   password,
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
			APIVersions: []string{"monitoring.coreos.com/v1beta1"},
		}
		h, err := NewHandler(logger, "reval", HandlerOptions{})
		require.NoError(t, err)
		h.newImageChecker = func() imageChecker {
			return fakeImageChecker{
				legacyImageReg: "nvcr.io",
			}
		}
		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassExtras, gotStr)
		}
	})

	t.Run("pass env creds", func(t *testing.T) {
		const helperName = "test-env"
		credhelper.RegisterAuthHelper(helperName, fakeEnvAuthHelper{
			host:     srvURL.Host,
			username: "$oauthtoken",
			password: password,
		})
		t.Cleanup(func() {
			credhelper.RegisterAuthHelper(helperName, noopAuthHelper{})
		})

		h, err := NewHandler(logger, "reval", HandlerOptions{})
		require.NoError(t, err)
		h.newImageChecker = func() imageChecker {
			return fakeImageChecker{publicImageReg: "nvcr.io"}
		}

		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:      true,
			ChartURL:    fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
			APIVersions: []string{"monitoring.coreos.com/v1beta1"},
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Empty(t, result.ValidationErrors)
		if assert.True(t, result.Valid) {
			gotStr := out.String()
			require.JSONEq(t, expPassExtras, gotStr)
		}
	})

	t.Run("fail bad types on skip validate", func(t *testing.T) {
		h, err := NewHandler(logger, "reval", HandlerOptions{
			SkipValidateObjects: true,
			SkipValidateImages:  true,
		})
		require.NoError(t, err)
		h.newImageChecker = func() imageChecker {
			return fakeImageChecker{
				badImageTags: []string{
					"nvcr.io/nginx:1.16.0",
				},
			}
		}
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			Values: json.RawMessage(`{
				"serviceAccount": {
					"create": true
				},
				"autoscaling": {
					"enabled": true
				},
				"ingress": {
					"enabled": true
				}
			}`),
			K8sVersion: "1.29.5",
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf(`unsupported types: ["autoscaling/v2.HorizontalPodAutoscaler" "networking.k8s.io/v1.Ingress"]`),
		}, result.ValidationErrors)
	})

	t.Run("fail bad types", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			Values: json.RawMessage(`{
				"serviceAccount": {
					"create": true
				},
				"autoscaling": {
					"enabled": true
				},
				"ingress": {
					"enabled": true
				}
			}`),
			K8sVersion: "1.29.5",
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf(`unsupported types: ["autoscaling/v2.HorizontalPodAutoscaler" "networking.k8s.io/v1.Ingress"]`),
		}, result.ValidationErrors)
	})

	t.Run("fail validate bad types", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		cfg := Config{
			Render:   false,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			Values: json.RawMessage(`{
				"serviceAccount": {
					"create": true
				},
				"autoscaling": {
					"enabled": true
				},
				"ingress": {
					"enabled": true
				}
			}`),
			K8sVersion: "1.29.5",
		}

		result, err := h.Run(context.Background(), cfg, nil)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf(`unsupported types: ["autoscaling/v2.HorizontalPodAutoscaler" "networking.k8s.io/v1.Ingress"]`),
		}, result.ValidationErrors)
	})

	t.Run("fail no matching port", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:         "sr-1234",
			ReleaseName:       "mini-service",
			K8sVersion:        "1.30.0",
			APIVersions:       []string{"monitoring.coreos.com/v1beta1"},
			TargetServiceName: "mini-service-test-chart-pod-sel",
			TargetServicePort: 81,
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []error{
			fmt.Errorf(`provided helm chart service "mini-service-test-chart-pod-sel" has no port 81 matching selected pod specs`),
			fmt.Errorf(`helm chart service "mini-service-test-chart-pod-sel" has no port 81`),
		}, result.ValidationErrors)
	})

	notFoundChartURL := fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.1")
	t.Run("fail not found", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: notFoundChartURL,
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf("run helm template: failed to fetch %s : 404 Not Found", notFoundChartURL),
		},
			result.ValidationErrors)
	})

	t.Run("fail bad auth", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{
					{
						Auths: map[string]common.RegistryAuth{
							srvHost:     {Auth: base64.StdEncoding.EncodeToString([]byte("foo:bar"))},
							"other.com": {Auth: basicAuth},
						},
					},
					{
						Auths: map[string]common.RegistryAuth{
							srvHost:      {Auth: base64.StdEncoding.EncodeToString([]byte("baz:buf"))},
							"other2.com": {Auth: basicAuth},
						},
					},
				},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.30.0",
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf(`run helm template: no credential was valid to pull chart "http://%s/test-chart-1.0.0.tgz", manually verify credentials are valid`, srvHost),
		},
			result.ValidationErrors)
	})

	t.Run("fail security", func(t *testing.T) {
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			Values: json.RawMessage(`{
				"securityContext": null,
				"podSecurityContext": null,
			}`),
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf("containers[\"test-chart\"].securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.readOnlyRootFilesystem must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.readOnlyRootFilesystem must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.readOnlyRootFilesystem must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.readOnlyRootFilesystem must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.readOnlyRootFilesystem must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.seccompProfile or podSpec.securityContext.seccompProfile must have type=RuntimeDefault"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.seccompProfile or podSpec.securityContext.seccompProfile must have type=RuntimeDefault"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.seccompProfile or podSpec.securityContext.seccompProfile must have type=RuntimeDefault"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.seccompProfile or podSpec.securityContext.seccompProfile must have type=RuntimeDefault"),
			fmt.Errorf("containers[\"test-chart\"].securityContext.seccompProfile or podSpec.securityContext.seccompProfile must have type=RuntimeDefault"),
		},
			result.ValidationErrors)
	})

	t.Run("fail bad images", func(t *testing.T) {
		h, err := NewHandler(logger, "reval", HandlerOptions{})
		require.NoError(t, err)
		h.newImageChecker = func() imageChecker {
			return fakeImageChecker{
				badImageTags: []string{
					"nvcr.io/nginx:1.16.0",
				},
			}
		}
		randReader = rand.New(rand.NewSource(1))
		out := &bytes.Buffer{}
		cfg := Config{
			Render:   true,
			ChartURL: fmt.Sprintf("%s/%s-%s.tgz", srv.URL, "test-chart", "1.0.0"),
			HelmRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{srvHost: {Auth: basicAuth}}}},
			},
			ImageRegistryAuthConfig: common.RegistryAuthConfig{
				K8sSecrets: []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{"nvcr.io": {Auth: basicAuth}}}},
			},
			Namespace:   "sr-1234",
			ReleaseName: "mini-service",
			K8sVersion:  "1.29.5",
		}

		result, err := h.Run(context.Background(), cfg, out)
		assert.NoError(t, err)
		assert.Equal(t, []error{
			fmt.Errorf(`unauthorized on image: "nvcr.io/nginx:1.16.0"`),
		},
			result.ValidationErrors)
	})
}

func Test_validatePodSpecSecurityFeatures(t *testing.T) {
	type spec struct {
		name      string
		podSpec   corev1.PodSpec
		expErrors []error
	}

	pm := corev1.UnmaskedProcMount
	cases := []spec{
		{
			name: "all fail",
			podSpec: corev1.PodSpec{
				HostNetwork:                  true,
				HostPID:                      true,
				HostIPC:                      true,
				AutomountServiceAccountToken: newBoolPtr(true),
				SecurityContext: &corev1.PodSecurityContext{
					RunAsUser:    newInt64Ptr(0),
					RunAsNonRoot: newBoolPtr(false),
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeUnconfined,
					},
					AppArmorProfile: &corev1.AppArmorProfile{
						Type: corev1.AppArmorProfileTypeUnconfined,
					},
					SELinuxOptions: &corev1.SELinuxOptions{
						Type: "container_admin_t",
						User: "admin",
						Role: "admin",
					},
				},
				InitContainers: []corev1.Container{
					{
						Name: "badinit1",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"CAP_SYS_ADMIN"},
							},
							Privileged:               newBoolPtr(true),
							AllowPrivilegeEscalation: newBoolPtr(true),
							ProcMount:                &pm,
							ReadOnlyRootFilesystem:   newBoolPtr(false),
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "bad1",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"CAP_SYS_ADMIN"},
							},
							Privileged:               newBoolPtr(true),
							AllowPrivilegeEscalation: newBoolPtr(true),
							ProcMount:                &pm,
							ReadOnlyRootFilesystem:   newBoolPtr(false),
						},
					},
					{
						Name: "bad2",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: nil,
						},
					},
					{
						Name: "good1",
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:    newInt64Ptr(1001),
							RunAsNonRoot: newBoolPtr(true),
							Capabilities: &corev1.Capabilities{
								Add:  []corev1.Capability{"NET_BIND_SERVICE"},
								Drop: []corev1.Capability{"ALL"},
							},
							Privileged:               newBoolPtr(false),
							AllowPrivilegeEscalation: newBoolPtr(false),
							ReadOnlyRootFilesystem:   newBoolPtr(true),
							SeccompProfile: &corev1.SeccompProfile{
								Type: corev1.SeccompProfileTypeRuntimeDefault,
							},
							AppArmorProfile: &corev1.AppArmorProfile{
								Type: corev1.AppArmorProfileTypeRuntimeDefault,
							},
							SELinuxOptions: &corev1.SELinuxOptions{
								Type: "container_kvm_t",
								User: "",
								Role: "",
							},
						},
					},
				},
			},
			expErrors: []error{
				fmt.Errorf("hostIPC must be false"),
				fmt.Errorf("hostNetwork must be false"),
				fmt.Errorf("hostPID must be false"),
				fmt.Errorf("automountServiceAccountToken must be false"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.capabilities.drop must contain \"ALL\""),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.capabilities.add must be unset or set to [\"NET_BIND_SERVICE\"]"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.privileged must not be true"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.allowPrivilegeEscalation must not be true"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.procMount must be unset or set to Default"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.readOnlyRootFilesystem must be set to true"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.runAsUser or podSpec.securityContext.runAsUser must not be 0"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.seccompProfile.type or podSpec.securityContext.seccompProfile.type must be set to one of RuntimeDefault/Localhost"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.appArmorProfile.type or podSpec.securityContext.appArmorProfile.type must be set to one of \"\"/RuntimeDefault/Localhost"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.seLinuxOptions.type or podSpec.securityContext.seLinuxOptions.type must be set to one of \"\"/container_t/container_init_t/container_kvm_t/container_engine_t"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.seLinuxOptions.user or podSpec.securityContext.seLinuxOptions.user must be unset"),
				fmt.Errorf("initContainers[\"badinit1\"].securityContext.seLinuxOptions.role or podSpec.securityContext.seLinuxOptions.role must be unset"),
				fmt.Errorf("containers[\"bad1\"].securityContext.capabilities.drop must contain \"ALL\""),
				fmt.Errorf("containers[\"bad1\"].securityContext.capabilities.add must be unset or set to [\"NET_BIND_SERVICE\"]"),
				fmt.Errorf("containers[\"bad1\"].securityContext.privileged must not be true"),
				fmt.Errorf("containers[\"bad1\"].securityContext.allowPrivilegeEscalation must not be true"),
				fmt.Errorf("containers[\"bad1\"].securityContext.procMount must be unset or set to Default"),
				fmt.Errorf("containers[\"bad1\"].securityContext.readOnlyRootFilesystem must be set to true"),
				fmt.Errorf("containers[\"bad1\"].securityContext.runAsUser or podSpec.securityContext.runAsUser must not be 0"),
				fmt.Errorf("containers[\"bad1\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
				fmt.Errorf("containers[\"bad1\"].securityContext.seccompProfile.type or podSpec.securityContext.seccompProfile.type must be set to one of RuntimeDefault/Localhost"),
				fmt.Errorf("containers[\"bad1\"].securityContext.appArmorProfile.type or podSpec.securityContext.appArmorProfile.type must be set to one of \"\"/RuntimeDefault/Localhost"),
				fmt.Errorf("containers[\"bad1\"].securityContext.seLinuxOptions.type or podSpec.securityContext.seLinuxOptions.type must be set to one of \"\"/container_t/container_init_t/container_kvm_t/container_engine_t"),
				fmt.Errorf("containers[\"bad1\"].securityContext.seLinuxOptions.user or podSpec.securityContext.seLinuxOptions.user must be unset"),
				fmt.Errorf("containers[\"bad1\"].securityContext.seLinuxOptions.role or podSpec.securityContext.seLinuxOptions.role must be unset"),
				fmt.Errorf("containers[\"bad2\"].securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]"),
				fmt.Errorf("containers[\"bad2\"].securityContext.readOnlyRootFilesystem must be set to true"),
				fmt.Errorf("containers[\"bad2\"].securityContext.runAsUser or podSpec.securityContext.runAsUser must not be 0"),
				fmt.Errorf("containers[\"bad2\"].securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true"),
				fmt.Errorf("containers[\"bad2\"].securityContext.seccompProfile.type or podSpec.securityContext.seccompProfile.type must be set to one of RuntimeDefault/Localhost"),
				fmt.Errorf("containers[\"bad2\"].securityContext.appArmorProfile.type or podSpec.securityContext.appArmorProfile.type must be set to one of \"\"/RuntimeDefault/Localhost"),
				fmt.Errorf("containers[\"bad2\"].securityContext.seLinuxOptions.type or podSpec.securityContext.seLinuxOptions.type must be set to one of \"\"/container_t/container_init_t/container_kvm_t/container_engine_t"),
				fmt.Errorf("containers[\"bad2\"].securityContext.seLinuxOptions.user or podSpec.securityContext.seLinuxOptions.user must be unset"),
				fmt.Errorf("containers[\"bad2\"].securityContext.seLinuxOptions.role or podSpec.securityContext.seLinuxOptions.role must be unset"),
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotErrors := validatePodSpecSecurityFeatures(tt.podSpec)
			assert.ElementsMatch(t, tt.expErrors, gotErrors)
		})
	}
}

func Test_validateHealthEndpoint(t *testing.T) {
	type spec struct {
		name              string
		svcPorts          []corev1.ServicePort
		targetServicePort int32
		containers        []corev1.Container
		expFound          bool
	}

	cases := []spec{
		{
			name: "found by name",
			svcPorts: []corev1.ServicePort{
				{
					Name:       "foo",
					TargetPort: intstr.FromString("http"),
					Port:       8000,
				},
				{
					Name:       "bar",
					TargetPort: intstr.FromString("grpc"),
					Port:       5001,
				},
			},
			targetServicePort: 8000,
			containers: []corev1.Container{
				{
					Name: "c1",
					Ports: []corev1.ContainerPort{
						{
							Name:          "grpc",
							ContainerPort: 5001,
						},
						{
							Name:          "http",
							ContainerPort: 8000,
						},
					},
				},
			},
			expFound: true,
		},
		{
			name: "found by number",
			svcPorts: []corev1.ServicePort{
				{
					Name:       "foo",
					TargetPort: intstr.FromInt(8000),
					Port:       80,
				},
				{
					Name:       "bar",
					TargetPort: intstr.FromInt(5001),
					Port:       5001,
				},
			},
			targetServicePort: 80,
			containers: []corev1.Container{
				{
					Name: "c1",
					Ports: []corev1.ContainerPort{
						{
							Name:          "grpc",
							ContainerPort: 5001,
						},
						{
							Name:          "http",
							ContainerPort: 8000,
						},
					},
				},
			},
			expFound: true,
		},
		{
			name: "found by port",
			svcPorts: []corev1.ServicePort{
				{
					Name: "foo",
					Port: 8000,
				},
				{
					Name: "bar",
					Port: 5001,
				},
			},
			targetServicePort: 8000,
			containers: []corev1.Container{
				{
					Name: "c1",
					Ports: []corev1.ContainerPort{
						{
							Name:          "grpc",
							ContainerPort: 5001,
						},
						{
							Name:          "http",
							ContainerPort: 8000,
						},
					},
				},
			},
			expFound: true,
		},
		{
			name: "no matching ports",
			svcPorts: []corev1.ServicePort{
				{
					Name:       "foo",
					TargetPort: intstr.FromInt(8000),
					Port:       8000,
				},
				{
					Name:       "bar",
					TargetPort: intstr.FromInt(5001),
					Port:       5001,
				},
			},
			targetServicePort: 8000,
			containers: []corev1.Container{
				{
					Name: "c1",
					Ports: []corev1.ContainerPort{
						{
							Name:          "grpc",
							ContainerPort: 5002,
						},
						{
							Name:          "http",
							ContainerPort: 8001,
						},
					},
				},
			},
			expFound: false,
		},
		{
			name: "no matching target ports",
			svcPorts: []corev1.ServicePort{
				{
					Name:       "foo",
					TargetPort: intstr.FromInt(8002),
					Port:       8000,
				},
				{
					Name:       "bar",
					TargetPort: intstr.FromInt(5001),
					Port:       5001,
				},
			},
			targetServicePort: 8000,
			containers: []corev1.Container{
				{
					Name: "c1",
					Ports: []corev1.ContainerPort{
						{
							Name:          "grpc",
							ContainerPort: 5002,
						},
						{
							Name:          "http",
							ContainerPort: 8001,
						},
					},
				},
			},
			expFound: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotFound := validateServicePort(
				&corev1.Service{Spec: corev1.ServiceSpec{Ports: tt.svcPorts}},
				tt.targetServicePort,
				corev1.PodSpec{Containers: tt.containers},
			)
			assert.Equal(t, tt.expFound, gotFound)
		})
	}
}

func Test_validateVolumes(t *testing.T) {
	type spec struct {
		name    string
		volumes []corev1.Volume
		expErrs []error
	}

	// Dynamically create a set of all volumes to test bad sources.
	badVolumes := []corev1.Volume{}
	rv := reflect.ValueOf(&corev1.Volume{}).Elem().FieldByName("VolumeSource")
	for i := range rv.NumField() {
		if f := rv.Field(i); f.CanSet() {
			switch f.Kind() {
			case reflect.Ptr:
				newVolume := &corev1.Volume{}
				nf := reflect.ValueOf(newVolume).Elem().FieldByName("VolumeSource").Field(i)
				newVolume.Name = strings.ToLower(f.Type().Elem().Name())
				nf.Set(reflect.New(f.Type().Elem()))
				if newVolume.Projected != nil {
					for i, projections := range [][]corev1.VolumeProjection{
						{{
							DownwardAPI: &corev1.DownwardAPIProjection{},
						}},
						{{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{},
						}},
						{{
							ClusterTrustBundle: &corev1.ClusterTrustBundleProjection{},
						}},
					} {
						vol := corev1.Volume{
							Name: fmt.Sprintf("projected-%d", i),
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: projections,
								},
							},
						}
						badVolumes = append(badVolumes, vol)
					}
				} else {
					badVolumes = append(badVolumes, *newVolume)
				}
			}
		}
	}
	sort.Slice(badVolumes, func(i, j int) bool {
		return badVolumes[i].Name < badVolumes[j].Name
	})

	cases := []spec{
		{
			name:    "no volumes",
			volumes: nil,
			expErrs: nil,
		},
		{
			name: "good volumes",
			volumes: []corev1.Volume{
				{
					Name: "secret",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{},
					},
				},
				{
					Name: "configmap",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{},
					},
				},
				{
					Name: "emptydir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "pvc",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{},
					},
				},
				{
					Name: "projected",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									Secret: &corev1.SecretProjection{},
								},
								{
									ConfigMap: &corev1.ConfigMapProjection{},
								},
							},
						},
					},
				},
			},
			expErrs: nil,
		},
		{
			name:    "bad volumes",
			volumes: badVolumes,
			expErrs: []error{errBadVolumeNames([]string{
				"awselasticblockstorevolumesource",
				"azurediskvolumesource",
				"azurefilevolumesource",
				"cephfsvolumesource",
				"cindervolumesource",
				"csivolumesource",
				"downwardapivolumesource",
				"ephemeralvolumesource",
				"fcvolumesource",
				"flexvolumesource",
				"flockervolumesource",
				"gcepersistentdiskvolumesource",
				"gitrepovolumesource",
				"glusterfsvolumesource",
				"hostpathvolumesource",
				"imagevolumesource",
				"iscsivolumesource",
				"nfsvolumesource",
				"photonpersistentdiskvolumesource",
				"portworxvolumesource",
				"projected-0",
				"projected-1",
				"projected-2",
				"quobytevolumesource",
				"rbdvolumesource",
				"scaleiovolumesource",
				"storageosvolumesource",
				"vspherevirtualdiskvolumesource",
			})},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotErrs := validateVolumes(tt.volumes)
			assert.Equal(t, tt.expErrs, gotErrs)
			for _, err := range gotErrs {
				_ = err.Error()
			}
		})
	}
}

func Test_validateHelmChartURL(t *testing.T) {
	assert.Equal(t,
		[]error{fmt.Errorf("invalid helm chart URL: parse \"http://a b\": invalid character \" \" in host name")},
		validateHelmChartURL("http://a b"),
	)

	assert.Equal(t,
		[]error{fmt.Errorf("helm chart URL must be absolute, got \"file:///foo/bar.txt\"")},
		validateHelmChartURL("file:///foo/bar.txt"),
	)

	assert.Empty(t,
		validateHelmChartURL("oci://foo.bar/a:b"),
	)

	assert.Empty(t,
		validateHelmChartURL("https://foo.bar"),
	)

	assert.Empty(t,
		validateHelmChartURL("https://helm.ngc.nvidiia.com/org"),
	)

	assert.Empty(t,
		validateHelmChartURL("https://helm.ngc.nvidia.com/org"),
	)

	assert.Empty(t,
		validateHelmChartURL("https://helm.stg.ngc.nvidia.com/org"),
	)
}

func Test_hasTypeMeta(t *testing.T) {
	type spec struct {
		data     string
		expHasTM bool
		expError string
	}

	for i, tt := range []spec{
		{
			data: `
apiVersion: v1
kind: Pod
`,
			expHasTM: true,
		},
		{
			data: `{
"apiVersion": "v1",
"kind": "Pod"
		}
`,
			expHasTM: true,
		},
		{
			data: `
kind: Pod
`,
			expHasTM: false,
		},
		{
			data: `
apiVersion: v1
`,
			expHasTM: false,
		},
		{
			data: `
# Comment only
`,
			expHasTM: false,
		},
		{
			data: `
Non yaml
`,
			expHasTM: false,
			expError: "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.TypeMeta",
		},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			hasTM, err := hasTypeMeta([]byte(tt.data))
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else if assert.NoError(t, err) {
				assert.Equal(t, tt.expHasTM, hasTM)
			}
		})
	}
}

func newTestHelmRepoServer(t *testing.T, testdataDir, password string) *httptest.Server {
	t.Helper()

	mux := &http.ServeMux{}
	srv := httptest.NewUnstartedServer(mux)
	addr := srv.Listener.Addr().String()

	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		gotUsername, gotPassword, ok := r.BasicAuth()
		if !ok {
			w.WriteHeader(401)
			return false
		}
		if gotUsername != "$oauthtoken" || gotPassword != password {
			w.WriteHeader(403)
			return false
		}
		return true
	}

	chartTemplate := template.Must(template.New("").Parse(`
apiVersion: v1
generated: ` + time.Now().Format(time.RFC3339Nano) + `
entries:
  {{- range $i, $chart := . }}
  {{$chart.Name}}:
  - created: {{$chart.Created}}
    digest: {{$chart.Digest}}
    name: {{$chart.Name}}
    urls:
    - http://` + addr + `/{{$chart.Name}}-1.0.0.tgz
    version: 1.0.0
  {{- end }}
`))

	type chart struct {
		Name    string
		Digest  string
		Created string
	}
	dirEntries, err := os.ReadDir(testdataDir)
	require.NoError(t, err)
	var charts []chart
	for _, dirEntry := range dirEntries {
		chartName := filepath.Clean(dirEntry.Name())

		tbuf := &bytes.Buffer{}
		tw := tar.NewWriter(tbuf)
		err := filepath.Walk(filepath.Join(testdataDir, dirEntry.Name()), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if match, err := filepath.Match("exp*.json", filepath.Base(path)); match || err != nil {
				return err
			}
			linkPath := strings.TrimPrefix(path, "testdata/")
			h, err := tar.FileInfoHeader(info, linkPath)
			if err != nil {
				return err
			}

			h.Name = linkPath
			if err = tw.WriteHeader(h); err != nil {
				return err
			}

			if info.Mode().IsDir() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
			return nil
		})
		require.NoError(t, err)
		err = tw.Close()
		require.NoError(t, err)

		gbuf := &bytes.Buffer{}
		gw := gzip.NewWriter(gbuf)
		_, err = gw.Write(tbuf.Bytes())
		require.NoError(t, err)
		err = gw.Close()
		require.NoError(t, err)

		mux.HandleFunc(fmt.Sprintf("/%s-1.0.0.tgz", chartName), func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(w, r) {
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gbuf.Bytes())
		})

		dig := sha256.Sum256(gbuf.Bytes())
		charts = append(charts, chart{
			Name:    chartName,
			Digest:  hex.EncodeToString(dig[:]),
			Created: time.Now().Format(time.RFC3339Nano),
		})
	}

	ctw := &bytes.Buffer{}
	err = chartTemplate.Execute(ctw, charts)
	require.NoError(t, err)

	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		w.Write(ctw.Bytes())
	})

	srv.Start()

	return srv
}

func newInt64Ptr(v int64) *int64 { return &v }
