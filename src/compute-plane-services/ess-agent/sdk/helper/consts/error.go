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

package consts

import "errors"

var (
	// ErrSealed is returned if an operation is performed on a sealed barrier.
	// No operation is expected to succeed before unsealing
	ErrSealed = errors.New("Vault is sealed")

	// ErrAPILocked is returned if an operation is performed when the API is
	// locked for the request namespace.
	ErrAPILocked = errors.New("API access to this namespace has been locked by an administrator")

	// ErrStandby is returned if an operation is performed on a standby Vault.
	// No operation is expected to succeed until active.
	ErrStandby = errors.New("Vault is in standby mode")

	// ErrPathContainsParentReferences is returned when a path contains parent
	// references.
	ErrPathContainsParentReferences = errors.New("path cannot contain parent references")

	// ErrInvalidWrappingToken is returned when checking for the validity of
	// a wrapping token that turns out to be invalid.
	ErrInvalidWrappingToken = errors.New("wrapping token is not valid or does not exist")
)
