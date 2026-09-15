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
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Connection provides the functionality of an LDAP connection,
// but through an interface.
type Connection interface {
	Bind(username, password string) error
	Close()
	Add(addRequest *ldap.AddRequest) error
	Modify(modifyRequest *ldap.ModifyRequest) error
	Del(delRequest *ldap.DelRequest) error
	Search(searchRequest *ldap.SearchRequest) (*ldap.SearchResult, error)
	StartTLS(config *tls.Config) error
	SetTimeout(timeout time.Duration)
	UnauthenticatedBind(username string) error
}

type PagingConnection interface {
	Connection
	SearchWithPaging(searchRequest *ldap.SearchRequest, pagingSize uint32) (*ldap.SearchResult, error)
}
