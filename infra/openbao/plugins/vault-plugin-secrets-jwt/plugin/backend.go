//
// Copyright 2021 Outfox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package jwtsecrets

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/errutil"
	"github.com/hashicorp/vault/sdk/helper/keysutil"
	"github.com/hashicorp/vault/sdk/logical"
	"gopkg.in/square/go-jose.v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	configPath  = "config"
	mainKeyName = "main"

	// Minimum cache size for transit backend
	minCacheSize = 10
)

type backend struct {
	*framework.Backend
	id               string
	lockManager      *keysutil.LockManager
	cachedConfig     *Config
	cachedConfigLock *sync.RWMutex
	idGen            uniqueIdGenerator
}

// Factory returns a new backend as logical.Backend.
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b, err := createBackend(conf)
	if err != nil {
		return nil, err
	}
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

func createBackend(conf *logical.BackendConfig) (*backend, error) {
	var b backend

	var err error
	b.lockManager, err = keysutil.NewLockManager(true, minCacheSize)
	if err != nil {
		return nil, err
	}

	b.id = conf.BackendUUID
	b.cachedConfigLock = new(sync.RWMutex)
	b.idGen = friendlyIdGenerator{}

	b.Backend = &framework.Backend{
		BackendType: logical.TypeLogical,
		Help:        strings.TrimSpace(backendHelp),
		PathsSpecial: &logical.Paths{
			Unauthenticated: []string{"jwks"},
		},
		Paths: framework.PathAppend(
			pathRole(&b),
			[]*framework.Path{
				pathConfig(&b),
				pathJwks(&b),
				pathSign(&b),
			},
		),
		Secrets: []*framework.Secret{
			b.token(),
		},
		InitializeFunc: b.initialize,
		PeriodicFunc:   b.periodic,
		Invalidate:     b.invalidate,
		Clean:          b.clean,
	}
	return &b, nil
}

func (b *backend) initialize(ctx context.Context, req *logical.InitializationRequest) error {

	if _, err := b.getConfig(ctx, req.Storage); err != nil {
		return err
	}

	b.Logger().Debug("Initialized")

	return nil
}

func (b *backend) periodic(ctx context.Context, req *logical.Request) error {

	config, err := b.getConfig(ctx, req.Storage)
	if err != nil {
		return err
	}

	policy, err := b.getPolicy(ctx, req.Storage, config, req.MountPoint)
	if err != nil {
		return err
	}

	return b.pruneKeyVersions(ctx, req.Storage, policy, config, req.MountPoint)
}

func (b *backend) invalidate(_ context.Context, key string) {
	if b.Logger().IsDebug() {
		b.Logger().Debug("invalidating key", "key", key)
	}
	switch {
	case strings.HasPrefix(key, "policy/"):
		name := strings.TrimPrefix(key, "policy/")
		b.lockManager.InvalidatePolicy(name)
	case strings.HasPrefix(key, configPath):
		b.cachedConfigLock.Lock()
		defer b.cachedConfigLock.Unlock()
		b.cachedConfig = nil
	}
}

func (b *backend) clean(_ context.Context) {
	// Nothing to do
}

func (b *backend) getPolicy(ctx context.Context, stg logical.Storage, config *Config, mount string) (*keysutil.Policy, error) {

	polReq := keysutil.PolicyRequest{
		Upsert:               true,
		Storage:              stg,
		Name:                 mainKeyName,
		Derived:              false,
		Convergent:           false,
		Exportable:           false,
		AllowPlaintextBackup: false,
	}

	var err error

	switch config.SignatureAlgorithm {
	case jose.RS256, jose.RS384, jose.RS512:
		switch config.RSAKeyBits {
		case 2048:
			polReq.KeyType = keysutil.KeyType_RSA2048
		case 3072:
			polReq.KeyType = keysutil.KeyType_RSA3072
		case 4096:
			polReq.KeyType = keysutil.KeyType_RSA4096
		default:
			err = errutil.InternalError{Err: "unsupported RSA key size"}
		}
	case jose.ES256:
		polReq.KeyType = keysutil.KeyType_ECDSA_P256
	case jose.ES384:
		polReq.KeyType = keysutil.KeyType_ECDSA_P384
	case jose.ES512:
		polReq.KeyType = keysutil.KeyType_ECDSA_P521
	default:
		err = errutil.InternalError{Err: "unknown/unsupported signature algorithm"}
	}

	if err != nil {
		return nil, err
	}

	policy, _, err := b.lockManager.GetPolicy(ctx, polReq, rand.Reader)
	if err != nil {
		return nil, err
	}

	if err := b.rotateIfNecessary(ctx, stg, policy, config, mount); err != nil {
		return nil, err
	}

	return policy, nil
}

func (b *backend) rotateIfNecessary(ctx context.Context, stg logical.Storage, policy *keysutil.Policy, config *Config, mount string) error {
	policy.Lock(true)
	defer policy.Unlock()

	latestKey, ok := policy.Keys[strconv.Itoa(policy.LatestVersion)]
	if !ok {
		return nil
	}

	if latestKey.CreationTime.Add(config.KeyRotationPeriod).After(time.Now()) {
		return nil
	}

	err := policy.Rotate(ctx, stg, rand.Reader)
	if err != nil {
		return err
	}

	b.lockManager.InvalidatePolicy(policy.Name)

	b.Logger().Info(fmt.Sprintf("Key Rotated: mount=%s", mount))

	return nil
}

func (b *backend) pruneKeyVersions(ctx context.Context, stg logical.Storage, policy *keysutil.Policy, config *Config, mount string) error {

	logger := b.Logger()

	if logger.IsDebug() {
		logger.Debug(fmt.Sprintf("Pruning Keys: mount=%s", mount))
	}

	policy.Lock(false)

	unexpiredVersion := intMax(policy.MinAvailableVersion, 1)
	for ; unexpiredVersion < policy.LatestVersion; unexpiredVersion += 1 {

		keyVersion, ok := policy.Keys[strconv.Itoa(unexpiredVersion)]
		if !ok {
			continue
		}

		keyExpiresAt := keyVersion.CreationTime.Add(config.KeyRotationPeriod).Add(config.TokenTTL)

		if logger.IsDebug() {
			logger.Debug(
				fmt.Sprintf(
					"Checking Key: mount=%s, version=%d created=%s, expires=%s",
					mount,
					unexpiredVersion,
					keyVersion.CreationTime.Format(time.RFC3339),
					keyExpiresAt.Format(time.RFC3339),
				),
			)
		}

		if keyExpiresAt.After(time.Now()) {
			break
		}
	}

	if unexpiredVersion == policy.MinAvailableVersion {
		policy.Unlock()
		return nil
	}

	policy.Unlock()
	policy.Lock(true)
	defer policy.Unlock()

	// Recheck after exclusive lock
	if unexpiredVersion == policy.MinAvailableVersion {
		return nil
	}

	// Ensure that cache doesn't get corrupted in error cases
	previousMinAvailableVersion := policy.MinAvailableVersion
	previousMinDecryptionVersion := policy.MinDecryptionVersion

	policy.MinAvailableVersion = unexpiredVersion
	policy.MinDecryptionVersion = unexpiredVersion

	if err := policy.Persist(ctx, stg); err != nil {
		policy.MinAvailableVersion = previousMinAvailableVersion
		policy.MinDecryptionVersion = previousMinDecryptionVersion
		return err
	}

	logger.Info(
		fmt.Sprintf(
			"Key Trimmed: mount=%s, latest=%d, min-available=%d, min-decryption=%d",
			mount,
			policy.LatestVersion,
			policy.MinAvailableVersion,
			policy.MinDecryptionVersion,
		),
	)

	return nil
}

const backendHelp = `
The JWT secrets engine signs JWTs.
`
