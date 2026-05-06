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
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	goctrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval/cache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/image-credential-helper/credhelper"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"go.uber.org/zap"
	orasregistry "oras.land/oras-go/v2/registry"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

func Test_validateImages(t *testing.T) {
	origPlain := plainHTTP
	plainHTTP = true
	t.Cleanup(func() { plainHTTP = origPlain })

	images := []string{
		"foo/bar:latest",
		"foo/buf:1.2.3",
		"foo/baz:latest",
	}
	legacyImages := []string{
		"myorg/myteam:latest",
		"myorg/myotherteam:1.2.3",
	}
	publicImages := []string{
		"some/public:latest",
	}
	regHost, auths, legacyAuths := createImageRegistry(t, images, legacyImages, publicImages)

	makeAuthSecrets := func(auths ...string) (authSecrets []common.RegistryAuthSecret) {
		for _, auth := range auths {
			authSecrets = append(authSecrets, common.RegistryAuthSecret{
				Auths: map[string]common.RegistryAuth{
					regHost: {Auth: auth},
				},
			})
		}
		return authSecrets
	}

	type spec struct {
		name           string
		images         []string
		imageAuths     []common.RegistryAuthSecret
		setup          func(t *testing.T)
		httpClient     *http.Client
		legacyAPIKey   string
		expErrPrefixes []string
		expSecrets     map[string]common.RegistryAuthSecret
	}

	for _, tt := range []spec{
		{
			name:       "all images",
			images:     images,
			imageAuths: makeAuthSecrets(auths...),
			expSecrets: map[string]common.RegistryAuthSecret{
				"workload-sr-foo-regcred-0": {
					Auths: map[string]common.RegistryAuth{regHost: {Auth: "MS11c2VybmFtZTowLXBhc3N3b3Jk"}},
				},
				"workload-sr-foo-regcred-1": {
					Auths: map[string]common.RegistryAuth{regHost: {Auth: "Mi11c2VybmFtZToxLXBhc3N3b3Jk"}},
				},
				"workload-sr-foo-regcred-2": {
					Auths: map[string]common.RegistryAuth{regHost: {Auth: "My11c2VybmFtZToyLXBhc3N3b3Jk"}},
				},
			},
		},
		{
			name:         "legacy api key",
			images:       legacyImages[:1],
			legacyAPIKey: legacyAuths[0],
			setup: func(t *testing.T) {
				t.Helper()
				nvcrHostReTmp := nvcrHostRe
				nvcrHostRe = regexp.MustCompile(".*")
				t.Cleanup(func() { nvcrHostRe = nvcrHostReTmp })
			},
			expSecrets: map[string]common.RegistryAuthSecret{
				"inference-container-pull-secret": {
					Auths: map[string]common.RegistryAuth{regHost: {Auth: "JG9hdXRodG9rZW46My1wYXNzd29yZA=="}},
				},
			},
		},
		{
			name:   "public",
			images: publicImages,
			httpClient: &http.Client{
				Transport: publicImageRoundTripper{inner: http.DefaultTransport},
			},
			expSecrets: map[string]common.RegistryAuthSecret{},
		},
		{
			name:         "no valid auth",
			images:       legacyImages[:1],
			legacyAPIKey: legacyAuths[1],
			imageAuths:   makeAuthSecrets(auths...),
			setup: func(t *testing.T) {
				t.Helper()
				nvcrHostReTmp := nvcrHostRe
				nvcrHostRe = regexp.MustCompile(".*")
				t.Cleanup(func() { nvcrHostRe = nvcrHostReTmp })
			},
			expErrPrefixes: []string{
				"no credential was valid to pull images, manually verify credentials are valid:",
			},
		},
		{
			name:         "malformed auth",
			images:       legacyImages[:1],
			legacyAPIKey: legacyAuths[1],
			imageAuths:   []common.RegistryAuthSecret{{Auths: map[string]common.RegistryAuth{regHost: {Auth: "foobar"}}}},
			setup: func(t *testing.T) {
				t.Helper()
				nvcrHostReTmp := nvcrHostRe
				nvcrHostRe = regexp.MustCompile(".*")
				t.Cleanup(func() { nvcrHostRe = nvcrHostReTmp })
			},
			expErrPrefixes: []string{
				"no credential was valid to pull images, manually verify credentials are valid:",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewExample()
			ctx := logIntoContext(context.Background(), logger)

			httpClient := tt.httpClient
			if httpClient == nil {
				httpClient = http.DefaultClient
			}

			if tt.setup != nil {
				tt.setup(t)
			}
			gotSecrets, gotErrs := validateImages(ctx,
				remoteImageChecker{
					credHelper: credhelper.NewCredHelper(),
					newImageClient: func(credsStore credentials.Store) orasauth.Client {
						return newORASClient(httpClient, credsStore)
					},
					imageCache: cache.NewImageCache(logger),
				},
				Config{
					Namespace:               "sr-foo",
					ReleaseName:             "mini-service",
					NGCAPIKey:               tt.legacyAPIKey,
					ImageRegistryAuthConfig: common.RegistryAuthConfig{K8sSecrets: tt.imageAuths},
				},
				tt.images,
			)
			if tt.expErrPrefixes != nil {
				assert.Len(t, gotErrs, len(tt.expErrPrefixes))
				for _, want := range tt.expErrPrefixes {
					assert.True(t, slices.ContainsFunc(gotErrs, func(e error) bool { return strings.HasPrefix(e.Error(), want) }))
				}
			} else {
				require.Empty(t, gotErrs)
				assert.Equal(t, tt.expSecrets, gotSecrets)
			}
		})
	}
}

func Test_validateImageCaching(t *testing.T) {
	origPlain := plainHTTP
	plainHTTP = true
	t.Cleanup(func() { plainHTTP = origPlain })

	publicImages := []string{
		"some/public:latest",
	}
	_, _, _ = createImageRegistry(t, nil, nil, publicImages)

	type spec struct {
		name           string
		imageSteps     [][]string
		imageAuths     []common.RegistryAuthSecret
		setup          func(t *testing.T)
		httpClient     *http.Client
		expCheckCounts map[string]int
		expHitCounts   map[string]int
	}

	for _, tt := range []spec{
		{
			name:       "public",
			imageSteps: [][]string{publicImages, publicImages},
			httpClient: &http.Client{
				Transport: publicImageRoundTripper{inner: http.DefaultTransport},
			},
			expCheckCounts: map[string]int{publicImages[0]: 2},
			expHitCounts:   map[string]int{publicImages[0]: 1},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewExample()
			ctx := logIntoContext(context.Background(), logger)

			httpClient := tt.httpClient
			if httpClient == nil {
				httpClient = http.DefaultClient
			}

			if tt.setup != nil {
				tt.setup(t)
			}
			imageCache := checkableImageCache{
				ImageCache:  cache.NewImageCache(logger),
				checkCounts: map[string]int{},
				hitCounts:   map[string]int{},
			}
			imgChecker := remoteImageChecker{
				credHelper: credhelper.NewCredHelper(),
				newImageClient: func(credsStore credentials.Store) orasauth.Client {
					return newORASClient(httpClient, credsStore)
				},
				imageCache: imageCache,
			}
			for _, images := range tt.imageSteps {
				_, gotErrs := validateImages(ctx,
					imgChecker,
					Config{
						Namespace:               "sr-foo",
						ReleaseName:             "mini-service",
						ImageRegistryAuthConfig: common.RegistryAuthConfig{K8sSecrets: tt.imageAuths},
					},
					images,
				)
				assert.Empty(t, gotErrs)
			}
			assert.Equal(t, tt.expCheckCounts, imageCache.checkCounts)
			assert.Equal(t, tt.expHitCounts, imageCache.hitCounts)
		})
	}
}

func Test_nvcrHostRe(t *testing.T) {
	assert.True(t, nvcrHostRe.MatchString("nvcr.io"))
	assert.True(t, nvcrHostRe.MatchString("stg.nvcr.io"))
	assert.False(t, nvcrHostRe.MatchString("notnvcr.io"))
}

func Test_parseReference(t *testing.T) {
	type spec struct {
		imageTag string
		expRef   orasregistry.Reference
		expError string
	}
	for _, tt := range []spec{
		{
			imageTag: "docker.io/foo/bar:mycustomtag",
			expRef: orasregistry.Reference{
				Registry:   "docker.io",
				Repository: "foo/bar",
				Reference:  "mycustomtag",
			},
		},
		{
			imageTag: "index.docker.io/foo/bar:mycustomtag",
			expRef: orasregistry.Reference{
				Registry:   "docker.io",
				Repository: "foo/bar",
				Reference:  "mycustomtag",
			},
		},
		{
			imageTag: "bar",
			expRef: orasregistry.Reference{
				Registry:   "docker.io",
				Repository: "library/bar",
				Reference:  "latest",
			},
		},
		{
			imageTag: "foo/bar",
			expRef: orasregistry.Reference{
				Registry:   "docker.io",
				Repository: "foo/bar",
				Reference:  "latest",
			},
		},
		{
			imageTag: "77777777777.dkr.ecr.us-west-2.amazonaws.com/project/foo/bar-2:0.0.2",
			expRef: orasregistry.Reference{
				Registry:   "77777777777.dkr.ecr.us-west-2.amazonaws.com",
				Repository: "project/foo/bar-2",
				Reference:  "0.0.2",
			},
		},
		{
			imageTag: "77777777777.dkr.ecr.us-west-2.amazonaws.com/project/foo/bar-2@sha256:a3d963eee91efe0297ce72aa52351551a5bc18e6498c4b0689e0edd3bd508de3",
			expRef: orasregistry.Reference{
				Registry:   "77777777777.dkr.ecr.us-west-2.amazonaws.com",
				Repository: "project/foo/bar-2",
				Reference:  "sha256:a3d963eee91efe0297ce72aa52351551a5bc18e6498c4b0689e0edd3bd508de3",
			},
		},
		{
			imageTag: "77777777777.dkr.ecr.us-west-2.amazonaws.com/project/foo/bar-2:0.0.2@sha256:a3d963eee91efe0297ce72aa52351551a5bc18e6498c4b0689e0edd3bd508de3",
			expRef: orasregistry.Reference{
				Registry:   "77777777777.dkr.ecr.us-west-2.amazonaws.com",
				Repository: "project/foo/bar-2",
				Reference:  "sha256:a3d963eee91efe0297ce72aa52351551a5bc18e6498c4b0689e0edd3bd508de3",
			},
		},
		{
			imageTag: "###",
			expError: "could not parse reference: ###",
		},
	} {
		t.Run(tt.imageTag, func(t *testing.T) {
			gotRef, gotErr := parseReference(tt.imageTag)
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
			} else {
				if assert.NoError(t, gotErr) {
					assert.Equal(t, tt.expRef, gotRef)
				}
			}
		})
	}
}

const testIsPublicHeader = "Test-Is-Public"

func createImageRegistry(t *testing.T, images, legacyImages, publicImages []string) (regHost string, auths, legacyAuths []string) {
	t.Helper()

	rsrv := goctrregistry.New(goctrregistry.Logger(log.New(io.Discard, "", log.Ldate)))

	// Create the unauthenticated server for pushing images.
	s := httptest.NewServer(rsrv)
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	require.NoError(t, err)
	regHost = u.Host

	imgPathToAuth := map[string]string{}
	for i, tag := range append(images, legacyImages...) {
		dst := fmt.Sprintf("%s/%s", regHost, tag)
		ref, err := name.ParseReference(dst)
		require.NoError(t, err)
		img, err := random.Image(1024, 2)
		require.NoError(t, err)
		err = remote.Push(ref, img)
		require.NoError(t, err)

		password := fmt.Sprint(i) + "-password"
		var username, auth string
		if i < len(images) {
			images[i] = dst
			username = fmt.Sprint(i+1) + "-username"
			auth = base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", username, password))
			auths = append(auths, auth)
		} else {
			legacyImages[i-len(images)] = dst
			auth = password
			legacyAuths = append(legacyAuths, password)
		}
		repo := ref.Context().RepositoryStr()
		imgPathToAuth[repo] = auth
	}

	for i, tag := range publicImages {
		dst := fmt.Sprintf("%s/%s", regHost, tag)
		ref, err := name.ParseReference(dst)
		require.NoError(t, err)
		img, err := random.Image(1024, 2)
		require.NoError(t, err)
		err = remote.Push(ref, img)
		require.NoError(t, err)

		publicImages[i] = dst
	}

	// Add auth
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(testIsPublicHeader) == "true" {
			rsrv.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		auth = strings.TrimSpace(strings.TrimPrefix(auth, "Basic"))
		imgPathSplit := strings.Split(r.URL.Path, "/")
		i := len(imgPathSplit) - 1
		for ; i > 0; i-- {
			switch imgPathSplit[i] {
			case "blobs", "manifests":
				goto done
			}
		}
	done:
		isAuth := auth != ""
		if i >= 2 {
			imgNamespace := strings.Join(imgPathSplit[2:i], "/")
			gotAuth, ok := imgPathToAuth[imgNamespace]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			b64dAuth, _ := base64.StdEncoding.DecodeString(auth)
			isAuth = gotAuth == auth || strings.HasSuffix(string(b64dAuth), ":"+gotAuth)
		}
		if !isAuth {
			if auth == "" {
				w.Header().Set("Www-Authenticate", "basic")
				w.WriteHeader(http.StatusUnauthorized)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}
		rsrv.ServeHTTP(w, r)
	})

	return regHost, auths, legacyAuths
}

type publicImageRoundTripper struct {
	inner http.RoundTripper
}

func (rt publicImageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(testIsPublicHeader, "true")
	return rt.inner.RoundTrip(req)
}

type checkableImageCache struct {
	cache.ImageCache
	checkCounts map[string]int
	hitCounts   map[string]int
}

func (ic checkableImageCache) Get(in cache.ImageCacheInput) bool {
	ic.checkCounts[in.ImageTag]++
	found := ic.ImageCache.Get(in)
	if found {
		ic.hitCounts[in.ImageTag]++
	}
	return found
}
