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

package logical

import (
	"crypto/tls"
)

// Connection represents the connection information for a request. This
// is present on the Request structure for credential backends.
type Connection struct {
	// RemoteAddr is the network address that sent the request.
	RemoteAddr string `json:"remote_addr"`

	// RemotePort is the network port that sent the request.
	RemotePort int `json:"remote_port"`

	// ConnState is the TLS connection state if applicable.
	ConnState *tls.ConnectionState `sentinel:""`
}
