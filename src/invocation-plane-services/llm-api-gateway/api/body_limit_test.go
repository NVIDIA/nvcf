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
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestRequestBodyLimitMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		limit         int64
		body          string
		unknownLength bool
		wantStatus    int
	}{
		{name: "disabled", limit: 0, body: strings.Repeat("a", 64), wantStatus: http.StatusOK},
		{name: "at limit", limit: 8, body: "12345678", wantStatus: http.StatusOK},
		{name: "declared length over limit", limit: 8, body: "123456789", wantStatus: http.StatusRequestEntityTooLarge},
		{name: "streamed body over limit", limit: 8, body: "123456789", unknownLength: true, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "streamed body at limit", limit: 8, body: "12345678", unknownLength: true, wantStatus: http.StatusOK},
		{name: "maximum limit", limit: math.MaxInt64, body: "12345678", unknownLength: true, wantStatus: http.StatusOK},
		{name: "empty body", limit: 8, body: "", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var handlerBody string
			e := echo.New()
			e.Use(NewRequestBodyLimitMiddleware(tt.limit))
			e.POST("/", func(c echo.Context) error {
				// Read twice to match middleware that buffers before the handler.
				first, err := captureRequestBody(c.Request())
				if err != nil {
					return err
				}
				second, err := io.ReadAll(c.Request().Body)
				if err != nil {
					return err
				}
				require.Equal(t, string(first), string(second))
				handlerBody = string(second)
				return c.NoContent(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.unknownLength {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == http.StatusOK {
				require.Equal(t, tt.body, handlerBody)
			}
		})
	}
}
