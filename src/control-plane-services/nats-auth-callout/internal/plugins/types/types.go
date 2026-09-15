/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package types

import (
	"time"

	"github.com/nats-io/jwt/v2"
)

// Request represents an incoming authentication request from NATS.
type Request struct {
	Account     string                    `json:"account"`
	PluginName  string                    `json:"pluginName"`
	Payload     string                    `json:"payload"`
	FullRequest *jwt.AuthorizationRequest `json:"-"` // primarily for nkey validation, not provided by users
}

// Result represents the result of authentication.
type Result struct {
	UserID      string        `json:"userId"`
	Account     string        `json:"account"`
	Permissions *Permissions  `json:"permissions"`
	TTL         time.Duration `json:"ttl"`
}

// Permissions defines the permissions for a plugin.
type Permissions struct {
	Publish   *PubPermissions `json:"publish,omitempty"`
	Subscribe *SubPermissions `json:"subscribe,omitempty"`
	Response  *ResponsePerms  `json:"response,omitempty"`
}

// PubPermissions defines publish permissions.
type PubPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// SubPermissions defines subscribe permissions.
type SubPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// ResponsePerms defines response permissions.
type ResponsePerms struct {
	MaxMsgs int           `json:"maxMsgs,omitempty"`
	TTL     time.Duration `json:"ttl,omitempty"`
}
