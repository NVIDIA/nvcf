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
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/image-credential-helper/credhelper"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sourcegraph/conc/pool"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/util/sets"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/credentials"

	corev1 "k8s.io/api/core/v1"
	orasregistry "oras.land/oras-go/v2/registry"
	orasremote "oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval/cache"
)

func collectImages(set sets.Set[string], podSpec corev1.PodSpec) {
	for _, c := range append(podSpec.InitContainers, podSpec.Containers...) {
		if img := strings.TrimSpace(c.Image); img != "" {
			set.Insert(c.Image)
		}
	}
}

// parseReference uses the go-containerregistry parser to fill in docker registry/default tag
// in imageTag, which the oras parser does not do correctly.
func parseReference(imageTag string) (orasregistry.Reference, error) {
	parsedRef, err := name.ParseReference(imageTag)
	if err != nil {
		return orasregistry.Reference{}, err
	}
	refCtx := parsedRef.Context()
	ref := orasregistry.Reference{
		Registry:   refCtx.RegistryStr(),
		Repository: refCtx.RepositoryStr(),
		Reference:  parsedRef.Identifier(),
	}
	const dockerHost = "docker.io"
	if ref.Registry == name.DefaultRegistry {
		ref.Registry = dockerHost
	}
	return ref, nil
}

var errNoValidImageCred = errors.New("no valid cred")

func validateImages(ctx context.Context, ic imageChecker, cfg Config, imageTags []string) (
	secrets map[string]common.RegistryAuthSecret,
	errs []error,
) {
	secrets = map[string]common.RegistryAuthSecret{}
	imgAuthCfg := cfg.ImageRegistryAuthConfig

	type result struct {
		ref orasregistry.Reference
		iat imageAuthType
		i   int
		err error
	}
	checkPool := pool.NewWithResults[result]().WithMaxGoroutines(5)
	for _, imageTag := range imageTags {
		ref, err := parseReference(imageTag)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		checkPool.Go(func() (res result) {
			res.ref = ref
			res.iat, res.i, res.err = ic.checkImageExists(ctx, ref, imgAuthCfg, cfg.NGCAPIKey)
			return res
		})
	}
	secretIdxSet := sets.New[int]()
	var goodRes []result
	var noValidCredImages []string
	for _, res := range checkPool.Wait() {
		if res.err != nil {
			if errors.Is(res.err, errNoValidImageCred) {
				noValidCredImages = append(noValidCredImages, res.ref.String())
			} else {
				errs = append(errs, res.err)
			}
			continue
		}
		// Only track indexes that refer to a real third-party secret.
		if res.i >= 0 {
			secretIdxSet.Insert(res.i)
		}
		goodRes = append(goodRes, res)
	}
	if len(errs) != 0 || len(noValidCredImages) != 0 {
		if len(noValidCredImages) != 0 {
			errs = append(errs, fmt.Errorf("no credential was valid to pull images, manually verify credentials are valid: %s",
				strings.Join(noValidCredImages, ", ")))
		}
		return nil, errs
	}

	for _, res := range goodRes {
		switch res.iat {
		case public:
			// Public image.
		case legacy:
			// Legacy API key.
			auth := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "$oauthtoken:%s", cfg.NGCAPIKey))
			const name = "inference-container-pull-secret"
			secrets[name] = common.RegistryAuthSecret{
				Auths: map[string]common.RegistryAuth{
					res.ref.Registry: {Auth: auth},
				},
			}
		default:
			if !secretIdxSet.Has(res.i) {
				continue
			}
			secretIdxSet.Delete(res.i)
			name := fmt.Sprintf("workload-%s-regcred-%d", cfg.Namespace, res.i)
			secrets[name] = imgAuthCfg.K8sSecrets[res.i]
		}
	}

	return secrets, nil
}

type imageChecker interface {
	checkImageExists(ctx context.Context, ref orasregistry.Reference, imgAuthCfg common.RegistryAuthConfig, legacyAPIKey string) (imageAuthType, int, error)
}

type imageAuthType int

const (
	_ imageAuthType = iota
	thirdParty
	legacy
	public
)

type remoteImageChecker struct {
	newImageClient func(credsStore credentials.Store) orasauth.Client
	credHelper     credhelper.CredHelper
	imageCache     cache.ImageCache
}

var nvcrHostRe = regexp.MustCompile(`^(.+\.)?nvcr\.io$`)

func (ic remoteImageChecker) checkImageExists(ctx context.Context, ref orasregistry.Reference, imgAuthCfg common.RegistryAuthConfig, legacyAPIKey string) (imageAuthType, int, error) {
	imgTag := ref.String()
	logger := logFromContext(ctx).With(zap.String("image", imgTag))

	// Create separate cache and creds store so concurrent use does not overwrite entries.
	credsStore := credhelper.NewCredentialStore()

	imageClient := ic.newImageClient(credsStore)
	// The resettable cache lets this code save an API call to re-auth after each API call
	// while getting around invalid cache entries for the same host.
	imageClientCache := newAuthCache()
	imageClient.Cache = imageClientCache

	repo := &orasremote.Repository{
		Reference: ref,
		Client:    &imageClient,
		PlainHTTP: plainHTTP,
	}
	isNVCR := nvcrHostRe.MatchString(ref.Registry)

	// Assume public image first since many images used in NVCF are, ex. etcd/rabbitmq.
	if ic.imageCache.Get(cache.ImageCacheInput{
		ImageTag: imgTag,
		Public:   true,
	}) {
		logger.Debug("Found public image in cache")
		return public, -1, nil
	}
	logger.Debug("Resolving public image")

	if _, err := repo.Resolve(ctx, imgTag); err == nil {
		logger.Debug("Found public image")
		ic.imageCache.Put(cache.ImageCacheInput{
			ImageTag: imgTag,
			Public:   true,
		})
		return public, -1, nil
	} else {
		logger.Debug("HEAD public image failed", zap.Error(err))
		imageClientCache.reset()
	}

	if legacyAPIKey != "" && isNVCR {
		logger.Debug("Resolving image with legacy creds")

		// Simulate login without making request to NGC, which uses Basic auth always.
		_ = credsStore.Put(ctx, ref.Registry, orasauth.Credential{Username: legacyUsername, Password: legacyAPIKey})
		if _, err := repo.Resolve(ctx, imgTag); err == nil {
			logger.Debug("Found image with legacy API key")
			return legacy, -1, nil
		} else {
			logger.Debug("HEAD image with legacy API key failed", zap.Error(err))
			imageClientCache.reset()
		}
	}

	var errs []error
	for i, secret := range imgAuthCfg.K8sSecrets {
		if auth, ok := secret.Auths[ref.Registry]; ok {
			iat, i, err := func() (imageAuthType, int, error) {
				defer imageClientCache.reset()

				logger := logger.With(zap.Int("credsIndex", i))
				logger.Debug("Resolving image")

				username, password, err := parseAuthToBasicCreds(logger, auth.Auth)
				if err != nil {
					return 0, -1, err
				}

				creds := credhelper.AuthHelperCredentials{
					Username: username,
					Password: password,
				}
				if username, password, err = ic.credHelper.GetRegistryCredentials(ctx, imgTag, creds); err != nil {
					logger.Debug("Failed to get registry creds", zap.Error(err))
					return 0, -1, err
				}

				if (username != "" || password != "") && !isNVCR {
					if err := loginToRegistry(ctx,
						imageClient, credsStore,
						ref.Host(), username, password); err != nil {
						logger.Debug("Failed to log in", zap.Error(err))
						return 0, -1, nil
					}
				} else if isNVCR {
					// Simulate login without making request to NGC, which uses Basic auth always.
					_ = credsStore.Put(ctx, ref.Registry, orasauth.Credential{Username: username, Password: password})
				}

				if _, err := repo.Resolve(ctx, imgTag); err == nil {
					logger.Debug("Found image")
					return thirdParty, i, nil
				} else {
					logger.Debug("HEAD image failed", zap.Error(err))
				}

				return 0, -1, nil
			}()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if iat > 0 {
				return iat, i, nil
			}
		}
	}

	// Attempt to fetch credentials using specific registry configuration from environment.
	// This process' environment must be configured to authenticate with the registry hoster's auth provider.
	iat, err := func() (imageAuthType, error) {
		defer imageClientCache.reset()

		logger := logger.With(zap.Bool("credsFromEnv", true))
		logger.Debug("Resolving image")

		creds := credhelper.AuthHelperCredentials{
			LoadFromEnv: true,
		}
		username, password, err := ic.credHelper.GetRegistryCredentials(ctx, imgTag, creds)
		if err != nil {
			logger.Debug("Failed to get registry creds", zap.Error(err))
			return 0, err
		}

		if err := loginToRegistry(ctx, imageClient, credsStore, ref.Host(), username, password); err != nil {
			logger.Debug("Failed to log in", zap.Error(err))
			return 0, nil
		}

		if _, err := repo.Resolve(ctx, imgTag); err == nil {
			logger.Debug("Found image")
			return thirdParty, nil
		} else {
			logger.Debug("HEAD image failed", zap.Error(err))
		}

		return 0, nil
	}()
	if err != nil {
		errs = append(errs, err)
	} else if iat > 0 {
		return iat, -1, nil
	}

	return 0, -1, errors.Join(append(errs, errNoValidImageCred)...)
}

// resettableAuthCache is a simple cache intended for single-threaded use
// which can be reset cheaply. Indended for use within checkImageExists() only.
type resettableAuthCache struct {
	m map[string]orasauth.Scheme
	t map[string]string
}

func newAuthCache() *resettableAuthCache {
	return &resettableAuthCache{
		m: map[string]orasauth.Scheme{},
		t: map[string]string{},
	}
}

func (c *resettableAuthCache) GetScheme(ctx context.Context, registry string) (orasauth.Scheme, error) {
	sch, ok := c.m[registry]
	if ok {
		return sch, nil
	}
	return orasauth.SchemeUnknown, errdef.ErrNotFound
}

func (c *resettableAuthCache) GetToken(ctx context.Context, registry string, scheme orasauth.Scheme, key string) (string, error) {
	sch, ok := c.m[registry]
	if ok && sch == scheme {
		if tok, ok := c.t[key]; ok {
			return tok, nil
		}
	}
	return "", errdef.ErrNotFound
}

func (c *resettableAuthCache) Set(ctx context.Context, registry string, scheme orasauth.Scheme, key string, fetch func(context.Context) (string, error)) (string, error) {
	tok, err := fetch(ctx)
	if err != nil {
		return "", err
	}
	c.m[registry] = scheme
	c.t[key] = tok
	return tok, nil
}

func (c *resettableAuthCache) reset() {
	for k := range c.m {
		delete(c.m, k)
	}
	for k := range c.t {
		delete(c.t, k)
	}
}
