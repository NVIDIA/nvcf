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
	"fmt"
)

type EncryptionError struct {
	Err     error  `json:"-"` // Actual error
	Message string `json:"message,omitempty"`
}

func ErrEncryptionFailed(err error, reason string) *EncryptionError {
	return &EncryptionError{
		Message: reason,
		Err:     err,
	}
}

func ErrEncryptionKeyNotFound(keyId string) *EncryptionError {
	return &EncryptionError{
		Err: fmt.Errorf("key (keyId: %s) not found", keyId),
	}
}

func ErrEncryptionKeySetMisconfigured(reason string) *EncryptionError {
	return &EncryptionError{
		Err:     fmt.Errorf("keyset misconfigured"),
		Message: reason,
	}
}

func (e *EncryptionError) Error() string {
	return e.Message + " - " + e.Err.Error()
}

func (e *EncryptionError) Equal(err error) bool {
	ee, ok := err.(*EncryptionError)
	if !ok {
		return false
	}
	return ee.Err.Error() == e.Err.Error()
}
