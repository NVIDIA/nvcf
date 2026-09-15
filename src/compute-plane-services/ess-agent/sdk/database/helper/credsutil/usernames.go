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
	"fmt"
	"strings"
	"time"
)

type CaseOp int

const (
	KeepCase CaseOp = iota
	Uppercase
	Lowercase
)

type usernameBuilder struct {
	displayName string
	roleName    string
	separator   string

	maxLen        int
	caseOperation CaseOp
}

func (ub usernameBuilder) makeUsername() (string, error) {
	userUUID, err := RandomAlphaNumeric(20, false)
	if err != nil {
		return "", err
	}

	now := fmt.Sprint(time.Now().Unix())

	username := joinNonEmpty(ub.separator,
		"v",
		ub.displayName,
		ub.roleName,
		userUUID,
		now)
	username = trunc(username, ub.maxLen)
	switch ub.caseOperation {
	case Lowercase:
		username = strings.ToLower(username)
	case Uppercase:
		username = strings.ToUpper(username)
	}

	return username, nil
}

type UsernameOpt func(*usernameBuilder)

func DisplayName(dispName string, maxLength int) UsernameOpt {
	return func(b *usernameBuilder) {
		b.displayName = trunc(dispName, maxLength)
	}
}

func RoleName(roleName string, maxLength int) UsernameOpt {
	return func(b *usernameBuilder) {
		b.roleName = trunc(roleName, maxLength)
	}
}

func Separator(sep string) UsernameOpt {
	return func(b *usernameBuilder) {
		b.separator = sep
	}
}

func MaxLength(maxLen int) UsernameOpt {
	return func(b *usernameBuilder) {
		b.maxLen = maxLen
	}
}

func Case(c CaseOp) UsernameOpt {
	return func(b *usernameBuilder) {
		b.caseOperation = c
	}
}

func ToLower() UsernameOpt {
	return Case(Lowercase)
}

func ToUpper() UsernameOpt {
	return Case(Uppercase)
}

func GenerateUsername(opts ...UsernameOpt) (string, error) {
	b := usernameBuilder{
		separator:     "_",
		maxLen:        100,
		caseOperation: KeepCase,
	}

	for _, opt := range opts {
		opt(&b)
	}

	return b.makeUsername()
}

func trunc(str string, l int) string {
	switch {
	case l > 0:
		if l > len(str) {
			return str
		}
		return str[:l]
	case l == 0:
		return str
	default:
		return ""
	}
}

func joinNonEmpty(sep string, vals ...string) string {
	if sep == "" {
		return strings.Join(vals, sep)
	}
	switch len(vals) {
	case 0:
		return ""
	case 1:
		return vals[0]
	}
	builder := &strings.Builder{}
	for _, val := range vals {
		if val == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString(sep)
		}
		builder.WriteString(val)
	}
	return builder.String()
}
