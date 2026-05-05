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

package errors

import (
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrEncryptionFailed(t *testing.T) {
	inner := stderrors.New("inner error")
	e := ErrEncryptionFailed(inner, "reason")
	assert.NotNil(t, e)
	assert.Contains(t, e.Error(), "inner error")
}

func TestErrEncryptionKeyNotFound(t *testing.T) {
	e := ErrEncryptionKeyNotFound("key-abc")
	assert.NotNil(t, e)
	assert.Contains(t, e.Error(), "key-abc")
}

func TestErrEncryptionKeySetMisconfigured(t *testing.T) {
	e := ErrEncryptionKeySetMisconfigured("bad config reason")
	assert.NotNil(t, e)
	assert.Contains(t, e.Error(), "keyset misconfigured")
}

func TestEncryptionError_Equal(t *testing.T) {
	e1 := ErrEncryptionKeyNotFound("key-1")
	e2 := ErrEncryptionKeyNotFound("key-1")
	e3 := ErrEncryptionKeyNotFound("key-2")
	assert.True(t, e1.Equal(e2))
	assert.False(t, e1.Equal(e3))
	assert.False(t, e1.Equal(stderrors.New("random")))
}
