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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestProviderHTTPError_UpstreamTransportError_HidesInternalDetails(t *testing.T) {
	t.Parallel()

	transportErr := &url.Error{
		Op:  "Post",
		URL: "http://router.internal.svc:8000/v1/responses",
		Err: errors.New("write tcp 10.0.0.1:36518->10.0.0.2:8000: write: broken pipe"),
	}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"url error", transportErr, http.StatusBadGateway, `{"message":"upstream request failed"}`},
		{"wrapped url error", fmt.Errorf("proxy: %w", transportErr), http.StatusBadGateway, `{"message":"upstream request failed"}`},
		{"http error passes through", echo.NewHTTPError(http.StatusTooManyRequests, "slow down"), http.StatusTooManyRequests, `{"message":"slow down"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), rec)

			err := providerHTTPError(tt.err)
			e.HTTPErrorHandler(err, c)

			require.Equal(t, tt.wantStatus, rec.Code)
			require.JSONEq(t, tt.wantBody, rec.Body.String())
			require.ErrorIs(t, err, tt.err)
		})
	}
}
