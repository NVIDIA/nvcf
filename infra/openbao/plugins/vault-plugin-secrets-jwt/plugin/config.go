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
	"encoding/json"
	"github.com/hashicorp/vault/sdk/helper/errutil"
	"github.com/hashicorp/vault/sdk/helper/keysutil"
	"github.com/hashicorp/vault/sdk/logical"
	"gopkg.in/square/go-jose.v2"
	"time"
)

// Default values for configuration options.
const (
	DefaultSignatureAlgorithm = jose.ES256
	DefaultRSAKeyBits         = 2048
	DefaultKeyRotationPeriod  = "2h0m0s"
	DefaultTokenTTL           = "3m0s"
	DefaultSetIAT             = true
	DefaultSetJTI             = true
	DefaultSetNBF             = true
	DefaultAudiencePattern    = ".*"
	DefaultSubjectPattern     = ".*"
	DefaultMaxAudiences       = -1
)

// DefaultAllowedClaims is the default value for the AllowedClaims config option.
// By default, only the 'sub' and 'aud' claims can be set by the caller.
var DefaultAllowedClaims = []string{"sub", "aud"}

var ReservedClaims = []string{"iss", "exp", "nbf", "iat", "jti"}
var ReservedHeaders = []string{"kid", "alg", "enc", "zip", "crit"}

var AllowedSignatureAlgorithmNames = []string{string(jose.ES256), string(jose.ES384), string(jose.ES512), string(jose.RS256), string(jose.RS384), string(jose.RS512)}
var AllowedRSAKeyBits = []int{2048, 3072, 4096}

// Config holds all configuration for the backend.
type Config struct {
	// SignatureAlgorithm is the signing algorithm to use.
	SignatureAlgorithm jose.SignatureAlgorithm

	// RSAKeyBits is size of generated RSA keys; only used when SignatureAlgorithm is one of the supported RSA algorithms.
	RSAKeyBits int

	// KeyRotationPeriod is how frequently a new key is created.
	KeyRotationPeriod time.Duration

	// TokenTTL defines how long a token is valid for after being signed.
	TokenTTL time.Duration

	// SetIat defines if the backend sets the 'iat' claim or not.
	SetIAT bool

	// SetJTI defines if the backend generates and sets the 'jti' claim or not.
	SetJTI bool

	// SetNBF defines if the backend sets the 'nbf' claim. If true, the claim will be set to the same as the 'iat' claim.
	SetNBF bool

	// AudiencePattern defines a regular expression (https://golang.org/pkg/regexp/) which must be matched by any incoming 'aud' claims.
	// If the audience claim is an array, each element in the array must match the pattern.
	AudiencePattern string

	// SubjectPattern defines a regular expression (https://golang.org/pkg/regexp/) which must be matched by any incoming 'sub' claims.
	SubjectPattern string

	// MaxAudiences defines the maximum number of strings in the 'aud' claim.
	MaxAudiences int

	// AllowedClaims defines which claims can be defined on the role or provided to the sign request to be set on the JWT.
	AllowedClaims []string

	// allowedClaimsMap is used to easily check if a claim is in the allowed claim set.
	allowedClaimsMap map[string]bool

	// AllowedHeaders defines which headers can be defined on the role or provided to the sign request to be set on the JWT.
	AllowedHeaders []string

	// allowedHeadersMap is used to easily check if a header is in the allowed header set.
	allowedHeadersMap map[string]bool
}

func (b *backend) getConfig(ctx context.Context, stg logical.Storage) (*Config, error) {
	b.cachedConfigLock.RLock()
	if b.cachedConfig != nil {
		defer b.cachedConfigLock.RUnlock()
		return b.cachedConfig.copy(), nil
	}

	b.cachedConfigLock.RUnlock()
	b.cachedConfigLock.Lock()
	defer b.cachedConfigLock.Unlock()

	// Double check somebody else didn't already cache it
	if b.cachedConfig != nil {
		return b.cachedConfig.copy(), nil
	}

	// Attempt to load config from storage & cache

	rawConfig, err := stg.Get(ctx, configPath)
	if err != nil {
		return nil, err
	}

	if rawConfig != nil {
		// Found it, finish load from storage
		conf := &Config{}
		if err := json.Unmarshal(rawConfig.Value, conf); err == nil {
			b.cachedConfig = conf.cache()
		} else {
			b.Logger().Warn("Failed to unmarshal config, resetting to default")
		}
	}
	if b.cachedConfig == nil {
		// Nothing found, initialize configuration to default and save
		b.cachedConfig = DefaultConfig(b.System())
		if err := b.saveConfigUnlocked(ctx, stg, b.cachedConfig); err != nil {
			return nil, err
		}

		b.Logger().Debug("Config Initialized")
	}

	return b.cachedConfig.copy(), nil
}

func (c *Config) copy() *Config {
	cc := *c
	return &cc
}

func (b *backend) saveConfig(ctx context.Context, stg logical.Storage, config *Config, mount string) error {
	b.cachedConfigLock.Lock()
	defer b.cachedConfigLock.Unlock()

	keyFormatChanged :=
		b.cachedConfig != nil &&
			(config.SignatureAlgorithm != b.cachedConfig.SignatureAlgorithm ||
				config.RSAKeyBits != b.cachedConfig.RSAKeyBits)

	if err := b.saveConfigUnlocked(ctx, stg, config); err != nil {
		return err
	}

	if !keyFormatChanged {
		return nil
	}

	b.Logger().Info("Key Format Rotation")

	policy, err := b.getPolicy(ctx, stg, config, mount)
	if err != nil {
		return err
	}

	policy.Lock(true)
	defer policy.Unlock()

	switch config.SignatureAlgorithm {
	case jose.RS256, jose.RS384, jose.RS512:
		switch config.RSAKeyBits {
		case 2048:
			policy.Type = keysutil.KeyType_RSA2048
		case 3072:
			policy.Type = keysutil.KeyType_RSA3072
		case 4096:
			policy.Type = keysutil.KeyType_RSA4096
		default:
			err = errutil.InternalError{Err: "unsupported RSA key size"}
		}
	case jose.ES256:
		policy.Type = keysutil.KeyType_ECDSA_P256
	case jose.ES384:
		policy.Type = keysutil.KeyType_ECDSA_P384
	case jose.ES512:
		policy.Type = keysutil.KeyType_ECDSA_P521
	default:
		err = errutil.InternalError{Err: "unknown/unsupported signature algorithm"}
	}

	if err != nil {
		return nil
	}

	defer b.lockManager.InvalidatePolicy(mainKeyName)

	return policy.Rotate(ctx, stg, rand.Reader)
}

func (b *backend) saveConfigUnlocked(ctx context.Context, stg logical.Storage, config *Config) error {

	entry, err := logical.StorageEntryJSON(configPath, config)
	if err != nil {
		return err
	}
	if err := stg.Put(ctx, entry); err != nil {
		return err
	}

	b.cachedConfig = config.cache()

	return nil
}

func (b *backend) clearConfig(ctx context.Context, stg logical.Storage) error {
	b.cachedConfigLock.Lock()
	defer b.cachedConfigLock.Unlock()

	if err := stg.Delete(ctx, configPath); err != nil {
		return err
	}

	b.cachedConfig = nil

	return nil
}

// DefaultConfig returns a default configuration.
func DefaultConfig(sys logical.SystemView) *Config {
	defaultKeyRotationPeriod, _ := time.ParseDuration(DefaultKeyRotationPeriod)
	defaultTokenTTL, _ := time.ParseDuration(DefaultTokenTTL)

	c := &Config{}
	c.SignatureAlgorithm = DefaultSignatureAlgorithm
	c.RSAKeyBits = DefaultRSAKeyBits
	c.KeyRotationPeriod = defaultKeyRotationPeriod
	c.TokenTTL = durationMin(defaultTokenTTL, sys.DefaultLeaseTTL())
	c.SetIAT = DefaultSetIAT
	c.SetJTI = DefaultSetJTI
	c.SetNBF = DefaultSetNBF
	c.AudiencePattern = DefaultAudiencePattern
	c.SubjectPattern = DefaultSubjectPattern
	c.MaxAudiences = DefaultMaxAudiences
	c.AllowedClaims = DefaultAllowedClaims
	return c
}

func (c *Config) cache() *Config {
	c.allowedClaimsMap = makeAllowedClaimsMap(c.AllowedClaims)
	c.allowedHeadersMap = makeAllowedClaimsMap(c.AllowedHeaders)
	return c
}

// turn the slice of allowed claims into a map to easily check if a given claim is in the set
func makeAllowedClaimsMap(allowedClaims []string) map[string]bool {
	newClaims := make(map[string]bool)
	for _, claim := range allowedClaims {
		newClaims[claim] = true
	}
	return newClaims
}
