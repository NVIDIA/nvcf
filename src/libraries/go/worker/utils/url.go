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

package utils

import "net/url"

// PortSafeUrl adds port 80 or 443 to http(s) urls for use with tcp or grpc dialers
func PortSafeUrl(uri string) (*url.URL, error) {
	normalized, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	if normalized.Port() == "" {
		port := ":443"
		if normalized.Scheme == "http" {
			port = ":80"
		}
		normalized.Host = normalized.Host + port
	}
	return normalized, nil
}
