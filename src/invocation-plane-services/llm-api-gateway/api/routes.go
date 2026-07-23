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
	"net/http"

	echo "github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Echo, handlers *Handlers) {
	e.GET("/healthz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	e.GET("/readyz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	group := e.Group("", rejectClientSuppliedPriority)
	handlers.AsOpenAIChatHandlers().RegisterRoutes(group)
	handlers.AsResponsesHandlers().RegisterRoutes(group)
	// proxy handlers only contain embedding route for now, but could be extended to other routes in the future
	handlers.AsOpenAIProxyHandlers().RegisterRoutes(group)
}
