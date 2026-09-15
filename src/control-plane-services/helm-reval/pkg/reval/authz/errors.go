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

package authz

import (
	"errors"
	"net/http"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/httpapi"
)

// ErrAuthorizationFailed is returned when an authorizer step rejects the request.
var ErrAuthorizationFailed = errors.New("authorization failed")

// ServeUnauthorized writes a 401 Unauthorized error response.
func ServeUnauthorized(r *http.Request, w http.ResponseWriter, err error) {
	httpapi.ServeSimpleError(r, w, "Unauthorized", err, http.StatusUnauthorized)
}
