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

package encrypt

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/go-jose/go-jose/v4"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

// JOSEEncryptionProvider provides the complete interface necessary to manage JOSE encryption/decryption
type JOSEEncryptionProvider interface {
	JOSEEncrypter
	JOSEDecrypter
}

// JOSEEncrypter provides the necessary interface for a jose-encrypter implementation
type JOSEEncrypter interface {
	Encrypt([]byte) (string, error)
}

// JOSEDecrypter provides the necessary interface for a jose-decrypter implementation
type JOSEDecrypter interface {
	Decrypt(string) ([]byte, error)
}

// AesJOSEEncryption implements JOSEEncryptionProvider for AES based key algorithm
type AesJOSEEncryption struct {
	keySource JOSEKeySource
	encrypter jose.Encrypter
}

// Encrypt implements JOSEEncrypter interface for AesJOSEEncryption
func (a *AesJOSEEncryption) Encrypt(bytes []byte) (string, error) {
	obj, err := a.encrypter.Encrypt(bytes)
	if err != nil {
		return "", errors.ErrEncryptionFailed(err, "cannot encrypt data")
	}
	return obj.CompactSerialize()
}

// Decrypt implements JOSEDecrypter interface for AesJOSEEncryption
func (a *AesJOSEEncryption) Decrypt(encData string) ([]byte, error) {
	jweData, err := jose.ParseEncrypted(encData, []jose.KeyAlgorithm{jose.A256GCMKW}, []jose.ContentEncryption{jose.A256GCM})
	if err != nil {
		return nil, errors.ErrEncryptionFailed(err, "cannot parse encrypted data")
	}
	keyID := jweData.Header.KeyID
	key, err := a.keySource.Load(keyID)
	if err != nil {
		return nil, errors.ErrEncryptionFailed(err, "failed to fetch decrypt key")
	}
	decData, err := jweData.Decrypt(key)
	if err != nil {
		return nil, errors.ErrEncryptionFailed(err, "cannot decrypt data")
	}
	return decData, nil
}

// NewAesJOSEEncryption creates a new AES based JOSE encrypter
func NewAesJOSEEncryption(keySource JOSEKeySource) (JOSEEncryptionProvider, error) {
	// Initialize key source
	if keySource == nil {
		return nil, errors.ErrEncryptionFailed(nil, "key loader not specified")
	}
	err := keySource.Init()
	if err != nil {
		return nil, errors.ErrEncryptionFailed(err, "failed init of key source")
	}

	// Initialize encrypter
	// TODO: Add support for different content-encryption algorithms and key sizes
	encrypter, err := jose.NewEncrypter(jose.A256GCM,
		jose.Recipient{Algorithm: jose.A256GCMKW, Key: (keySource.GetActiveKey().Key).([]byte)},
		(&jose.EncrypterOptions{}).WithType("JWT").WithHeader("kid", keySource.GetActiveKey().KeyID))
	if err != nil {
		return nil, errors.ErrEncryptionFailed(err, "encrypter instantiation failed")
	}

	return &AesJOSEEncryption{keySource: keySource, encrypter: encrypter}, nil
}

// JWKSet maintains all the keys configuration from key-file
// and in a format that allows us to use jose's library functions effectively
type JWKSet struct {
	ActiveKeyID string `json:"activeKeyId"`
	jose.JSONWebKeySet
}

// JOSEKeySource is an interface that provides necessary interface
// to support fetching jose keys with the support for key rotation
type JOSEKeySource interface {
	// Init function should be called once to initialize based on the provided config
	// This is a good place to add any implementation specific validation
	Init() error
	// GetActiveKey returns the active key used for encryption
	GetActiveKey() jose.JSONWebKey
	// Load return the key looked up by keyID. This is particularly useful during decryption
	Load(keyID string) (jose.JSONWebKey, error)
}

// JOSEKeyFileSource loads keys from a file
type JOSEKeyFileSource struct {
	// KeysFilePath maintains the fil path that maintains the keys for encryption
	// Keys-file needs to maintain keys in this format {"activeKeyId":<>, keys:[{"kty":<>,"kid":<>,"k":<>,"alg":<>}]]}
	KeysFilePath string
	// JwkSet maintains all the keys configuration from key-file
	// and in a format that allows us to use jose's library functions effectively
	JwkSet JWKSet
}

type Key struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	K   string `json:"k"`
	Alg string `json:"alg"`
}

type Keys struct {
	ActiveKeyId string `json:"activeKeyId"`
	Keys        []Key  `json:"keys"`
}

// Init initializes and validates the key configuration
// TODO: Explore and add support for automatically initing once during Load function using sync.
func (js *JOSEKeyFileSource) Init() error {
	parsedJwks := Keys{}
	// Init and unmarshal the key-file
	keySet, err := os.ReadFile(js.KeysFilePath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(keySet, &parsedJwks); err != nil {
		return err
	}

	// This extra step is required because while reading a file, we can't read key.K as byte
	// Using JWKSet directly for parsing doesn't work because the jose library doesn't expose enough customization
	// to allow the key to be read as string rather than a byte array which has implications down the lane during encryption.
	js.JwkSet.ActiveKeyID = parsedJwks.ActiveKeyId
	var jwksKeys []jose.JSONWebKey
	for _, key := range parsedJwks.Keys {
		jwksKeys = append(jwksKeys, jose.JSONWebKey{
			Key:       []byte(key.K),
			KeyID:     key.Kid,
			Algorithm: key.Alg,
		})
	}
	js.JwkSet.JSONWebKeySet.Keys = jwksKeys //nolint:staticcheck // QF1008: explicit selector for clarity

	// Validate the read config
	return js.validateConfig()
}

// validateConfig validates that the key-config is in line with what is expected of this file source configuration
func (js *JOSEKeyFileSource) validateConfig() error {
	// Validate that there is only one key for each key-id
	for _, jwk := range js.JwkSet.JSONWebKeySet.Keys { //nolint:staticcheck // QF1008: explicit selector for clarity
		keys := js.JwkSet.JSONWebKeySet.Key(jwk.KeyID) //nolint:staticcheck // QF1008: explicit selector for clarity
		if len(keys) != 1 {
			return errors.ErrEncryptionKeySetMisconfigured(fmt.Sprintf("mismatched number of keys for key-id(%s)", jwk.KeyID))
		}
	}
	// Validate that the active-key-id config is present
	keys := js.JwkSet.JSONWebKeySet.Key(js.JwkSet.ActiveKeyID) //nolint:staticcheck // QF1008: explicit selector for clarity
	if len(keys) == 0 {
		return errors.ErrEncryptionKeyNotFound(js.JwkSet.ActiveKeyID)
	}

	return nil
}

// Load function loads a specific key-id details from that key source
func (js *JOSEKeyFileSource) Load(keyID string) (jose.JSONWebKey, error) {
	keys := js.JwkSet.JSONWebKeySet.Key(keyID) //nolint:staticcheck // QF1008: explicit selector for clarity
	if len(keys) == 0 {
		return jose.JSONWebKey{}, errors.ErrEncryptionKeyNotFound(keyID)
	}

	// NOTE:
	//   Since we validate that the loaded keys have only key configured for each key-id,
	//   there can only be one key returned.
	return keys[0], nil
}

// GetActiveKey loads the active key from the key source
func (js *JOSEKeyFileSource) GetActiveKey() jose.JSONWebKey {
	// Init() function validates that exactly one instance of active key is present.
	// Hence we can skip error checking here.
	activeKey, _ := js.Load(js.JwkSet.ActiveKeyID)
	return activeKey
}
