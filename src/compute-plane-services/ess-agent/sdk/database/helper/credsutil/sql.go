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
	"time"

	"github.com/hashicorp/vault/sdk/database/dbplugin"
)

const (
	NoneLength int = -1
)

// SQLCredentialsProducer implements CredentialsProducer and provides a generic credentials producer for most sql database types.
type SQLCredentialsProducer struct {
	DisplayNameLen    int
	RoleNameLen       int
	UsernameLen       int
	Separator         string
	LowercaseUsername bool
}

func (scp *SQLCredentialsProducer) GenerateCredentials(ctx context.Context) (string, error) {
	password, err := scp.GeneratePassword()
	if err != nil {
		return "", err
	}
	return password, nil
}

func (scp *SQLCredentialsProducer) GenerateUsername(config dbplugin.UsernameConfig) (string, error) {
	caseOp := KeepCase
	if scp.LowercaseUsername {
		caseOp = Lowercase
	}
	return GenerateUsername(
		DisplayName(config.DisplayName, scp.DisplayNameLen),
		RoleName(config.RoleName, scp.RoleNameLen),
		Case(caseOp),
		Separator(scp.Separator),
		MaxLength(scp.UsernameLen),
	)
}

func (scp *SQLCredentialsProducer) GeneratePassword() (string, error) {
	password, err := RandomAlphaNumeric(20, true)
	if err != nil {
		return "", err
	}

	return password, nil
}

func (scp *SQLCredentialsProducer) GenerateExpiration(ttl time.Time) (string, error) {
	return ttl.Format("2006-01-02 15:04:05-0700"), nil
}
