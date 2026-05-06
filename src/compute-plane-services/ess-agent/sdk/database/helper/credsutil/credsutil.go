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

package credsutil

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-secure-stdlib/base62"
	"github.com/hashicorp/vault/sdk/database/dbplugin"
)

// CredentialsProducer can be used as an embedded interface in the Database
// definition. It implements the methods for generating user information for a
// particular database type and is used in all the builtin database types.
type CredentialsProducer interface {
	GenerateCredentials(context.Context) (string, error)
	GenerateUsername(dbplugin.UsernameConfig) (string, error)
	GeneratePassword() (string, error)
	GenerateExpiration(time.Time) (string, error)
}

const (
	reqStr    = `A1a-`
	minStrLen = 10
)

// RandomAlphaNumeric returns a random string of characters [A-Za-z0-9-]
// of the provided length. The string generated takes up to 4 characters
// of space that are predefined and prepended to ensure password
// character requirements. It also requires a min length of 10 characters.
func RandomAlphaNumeric(length int, prependA1a bool) (string, error) {
	if length < minStrLen {
		return "", fmt.Errorf("minimum length of %d is required", minStrLen)
	}

	var prefix string
	if prependA1a {
		prefix = reqStr
	}

	randomStr, err := base62.Random(length - len(prefix))
	if err != nil {
		return "", err
	}

	return prefix + randomStr, nil
}
