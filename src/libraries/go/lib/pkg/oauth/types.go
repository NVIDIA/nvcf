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

package oauth

import "time"

type JWT struct {
	Issuer            string `json:"iss"`
	TokenType         string `json:"token_type"`
	Subject           string `json:"sub"`
	AuthorizedParties string `json:"azp"`
	Service           struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"service"`
	JWTID      string   `json:"jti"`
	Audience   []string `json:"aud"`
	Scopes     []string `json:"scopes"`
	Expiration int64    `json:"exp"`
	IssuedAt   int64    `json:"iat"`
}

func (jwt JWT) ExpirationTime() time.Time {
	return time.Unix(jwt.Expiration, 0)
}

func (jwt JWT) IssuedAtTime() time.Time {
	return time.Unix(jwt.IssuedAt, 0)
}
