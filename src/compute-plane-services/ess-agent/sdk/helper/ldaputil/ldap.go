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

package ldaputil

import (
	"crypto/tls"

	"github.com/go-ldap/ldap/v3"
)

func NewLDAP() LDAP {
	return &ldapIfc{}
}

// LDAP provides ldap functionality, but through an interface
// rather than statically. This allows faking it for tests.
type LDAP interface {
	Dial(network, addr string) (Connection, error)
	DialTLS(network, addr string, config *tls.Config) (Connection, error)
}

type ldapIfc struct{}

func (l *ldapIfc) Dial(network, addr string) (Connection, error) {
	return ldap.Dial(network, addr)
}

func (l *ldapIfc) DialTLS(network, addr string, config *tls.Config) (Connection, error) {
	return ldap.DialTLS(network, addr, config)
}
