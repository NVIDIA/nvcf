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

// Package models defines API response models
package models

import "time"

// PingResponse Ping interface response structure
type PingResponse struct {
	Message   string `json:"message" example:"pong" maxLength:"100"`
	Service   string `json:"service" example:"nvcf-nats-auth-callout-service" maxLength:"100"`
	Timestamp int64  `json:"timestamp" example:"1640995200" format:"int64" minimum:"0" maximum:"9999999999"`
}

// HealthResponse Health check interface response structure
type HealthResponse struct {
	Status string `json:"status" example:"ok" enums:"ok,degraded,down" maxLength:"20"`
}

// ErrorResponse Error response structure
type ErrorResponse struct {
	Error   string `json:"error" example:"request parameter error" maxLength:"200"`
	Code    int    `json:"code" example:"400" format:"int32" minimum:"100" maximum:"599"`
	Message string `json:"message" example:"detailed error message" maxLength:"500"`
}

// APIResponse Generic API response structure
type APIResponse struct {
	Success   bool           `json:"success" example:"true"`
	Data      any            `json:"data,omitempty"`
	Error     *ErrorResponse `json:"error,omitempty"`
	Timestamp time.Time      `json:"timestamp" example:"2023-01-01T00:00:00Z"`
}
