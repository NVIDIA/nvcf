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
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/test/mocks"
)

const (
	invalidFilePath                 = "/invalid/file/path"
	testInvalidKeyFormatFilePath    = "../test/keys/aes/invalidkeyformat.json"
	testValidKeyFilePath            = "../test/keys/aes/validkey.json"
	testInvalidActiveKeyFilePath    = "../test/keys/aes/invalidactivekey.json"
	testInvalidMultipleKeysFilePath = "../test/keys/aes/invalidmultiplekeys.json"
)

var (
	testError           = fmt.Errorf("test error")
	testDecryptedObject = []byte("test-object")
)

func TestJOSEKeyFileSource_Init(t *testing.T) {
	type testCase struct {
		desc        string
		filePath    string
		expectedErr error
	}
	testCases := []testCase{
		{
			desc:        "Case: file path is invalid",
			filePath:    invalidFilePath,
			expectedErr: &fs.PathError{Op: "open", Path: invalidFilePath, Err: syscall.ENOENT},
		},
		{
			desc:        "Case: invalid file format",
			filePath:    testInvalidKeyFormatFilePath,
			expectedErr: testError,
		},
		{
			desc:        "Case: key file with multiple keys for same key-id (unsupported case)",
			filePath:    testInvalidMultipleKeysFilePath,
			expectedErr: nverrors.ErrEncryptionKeySetMisconfigured("some-err"),
		},
		{
			desc:        "Case: key file with missing active key details",
			filePath:    testInvalidActiveKeyFilePath,
			expectedErr: nverrors.ErrEncryptionKeyNotFound("active-key"),
		},
		{
			desc:        "Case: valid key input file",
			filePath:    testValidKeyFilePath,
			expectedErr: nil,
		},
	}

	for _, tc := range testCases {
		joseKeyFileSource := &JOSEKeyFileSource{KeysFilePath: tc.filePath}
		var encErr *nverrors.EncryptionError
		err := joseKeyFileSource.Init()
		if tc.expectedErr == nil {
			assert.Nil(t, err, tc.desc)
		} else if ok := errors.As(tc.expectedErr, &encErr); !ok {
			assert.NotNil(t, err, tc.desc)
		} else {
			assert.True(t, encErr.Equal(err),
				fmt.Sprintf("tc: %s, actualErr: %+v, expectedErr: %+v",
					tc.desc, err, encErr))
		}
	}
}

func TestJOSEKeyFileSource(t *testing.T) {
	keyFileSource := &JOSEKeyFileSource{JwkSet: JWKSet{
		ActiveKeyID: "active-key",
		JSONWebKeySet: jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				{
					KeyID: "active-key",
					Key:   "active-key",
				},
				{
					KeyID: "another-key",
					Key:   "another-key",
				},
			},
		},
	}}

	// Test that Load() function returns the right key
	desc := "Case: key is present in jwks"
	key, err := keyFileSource.Load("active-key")
	assert.Nil(t, err, desc)
	assert.Equal(t, jose.JSONWebKey{KeyID: "active-key", Key: "active-key"}, key, desc)
	desc = "Case: key is not present in jwks"
	key, err = keyFileSource.Load("some-key")
	assert.NotNil(t, err, desc)
	assert.Equal(t, jose.JSONWebKey{}, key, desc)

	// Test that GetActiveKey() function returns the active key
	desc = "Case: active key is present and loaded from jwks"
	key = keyFileSource.GetActiveKey()
	assert.Equal(t, jose.JSONWebKey{KeyID: "active-key", Key: "active-key"}, key, desc)
}

type testKeySource struct {
	key jose.JSONWebKey
	err error
}

func (t testKeySource) Init() error {
	return t.err
}

func (t testKeySource) GetActiveKey() jose.JSONWebKey {
	return t.key
}

func (t testKeySource) Load(keyID string) (jose.JSONWebKey, error) {
	return t.key, t.err
}

func TestNewAesJOSEEncryption(t *testing.T) {
	desc := "Case: nil key source provided"
	provider, err := NewAesJOSEEncryption(nil)
	assert.NotNil(t, err, desc)
	assert.Nil(t, provider, desc)

	desc = "Case: key source init failed"
	provider, err = NewAesJOSEEncryption(&testKeySource{err: testError})
	assert.Equal(t, nverrors.ErrEncryptionFailed(testError, "failed init of key source"), err, desc)
	assert.Nil(t, provider, desc)
}

func TestAesJOSEEncryption_EncryptDecrypt(t *testing.T) {
	mockEncrypter := &mocks.JOSEEncryption{}
	provider := &AesJOSEEncryption{encrypter: mockEncrypter}
	desc := "Case: encrypter failure"
	mockEncrypter.On("Encrypt", testDecryptedObject).Return(nil, testError).Once()
	encObj, err := provider.Encrypt(testDecryptedObject)
	assert.NotNil(t, err, desc)
	assert.Equal(t, "", encObj, desc)
	mock.AssertExpectationsForObjects(t, mockEncrypter)

	desc = "Case: encrypter failure during compact serialization"
	mockEncrypter.On("Encrypt", testDecryptedObject).Return(&jose.JSONWebEncryption{}, nil).Once()
	encObj, err = provider.Encrypt(testDecryptedObject)
	assert.NotNil(t, err, desc)
	assert.Equal(t, "", encObj, desc)
	mock.AssertExpectationsForObjects(t, mockEncrypter)

	// Create a real provider for testing encrypt and decrypt path
	realProvider, err := NewAesJOSEEncryption(&JOSEKeyFileSource{KeysFilePath: testValidKeyFilePath})
	require.Nil(t, err, desc)
	require.NotNil(t, realProvider, desc)

	desc = "Case: encrypter encrypt-decrypt success"
	encrypted, err := realProvider.Encrypt(testDecryptedObject)
	assert.Nil(t, err, desc)
	decrypted, err := realProvider.Decrypt(encrypted)
	assert.Nil(t, err, desc)
	assert.Equal(t, testDecryptedObject, decrypted, desc)

	desc = "Case: encrypter decrypt success with non-active-key"
	encrypted, err = realProvider.Encrypt(testDecryptedObject)
	assert.Nil(t, err, desc)
	// The testValidKeyFilePath has two keys - active-key, another-key
	aesProvider, ok := realProvider.(*AesJOSEEncryption)
	require.True(t, ok, desc)
	keySource := aesProvider.keySource.(*JOSEKeyFileSource)
	require.True(t, ok, desc)
	// Make sure the current activeKeyId is "active-key"
	require.Equal(t, "active-key", aesProvider.keySource.GetActiveKey().KeyID, desc)
	// Setting the activeKeyId to another-key simulates a key rotation scenario
	keySource.JwkSet.ActiveKeyID = "another-key"
	// At this point, decrypt will pick a non-active key determined by the key-id in the encrypted-object
	decrypted, err = realProvider.Decrypt(encrypted)
	assert.Nil(t, err, desc)
	assert.Equal(t, testDecryptedObject, decrypted, desc)
}
