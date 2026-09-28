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

package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	echo "github.com/labstack/echo/v4"
)

// NewRequestBodyLimitMiddleware buffers the request body up to limit bytes and
// rejects larger bodies with 413 before any handler reads them. Handlers keep
// reading the buffered copy. A limit of zero or less disables the check.
func NewRequestBodyLimitMiddleware(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if limit <= 0 {
			return next
		}
		return func(c echo.Context) error {
			req := c.Request()
			if req.Body == nil || req.Body == http.NoBody {
				return next(c)
			}
			if req.ContentLength > limit {
				return echo.ErrStatusRequestEntityTooLarge
			}

			body, err := io.ReadAll(io.LimitReader(req.Body, limit))
			if err == nil && int64(len(body)) == limit {
				// Probe for one more byte instead of reading limit+1, which
				// overflows at math.MaxInt64.
				var extra [1]byte
				var n int
				n, err = io.ReadFull(req.Body, extra[:])
				if n > 0 {
					_ = req.Body.Close()
					return echo.ErrStatusRequestEntityTooLarge
				}
				if errors.Is(err, io.EOF) {
					err = nil
				}
			}
			_ = req.Body.Close()
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "read request body: "+err.Error())
			}

			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			return next(c)
		}
	}
}
