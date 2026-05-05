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

// Error type constants for authentication errors.
const (
	ErrTypeInvalidToken   = "invalid_token"
	ErrTypeExpiredToken   = "expired_token"
	ErrTypeUnauthorized   = "unauthorized"
	ErrTypeInternalError  = "internal_error"
	ErrTypeRateLimit      = "rate_limit"
	ErrTypeInvalidRequest = "invalid_request"
	ErrTypePluginError    = "plugin_error"
)

// Error represents authentication errors.
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// NewAuthError creates a new authentication error.
func NewAuthError(errType, message string, code int) *Error {
	return &Error{
		Type:    errType,
		Message: message,
		Code:    code,
	}
}

// Error implements the error interface.
func (e *Error) Error() string {
	return e.Message
}

// IsRetryable returns true if this error should be retried.
func (e *Error) IsRetryable() bool {
	switch e.Type {
	case ErrTypeRateLimit, ErrTypeInternalError:
		return true
	default:
		return false
	}
}
