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
	"fmt"
	"net/http"

	echo "github.com/labstack/echo/v4"
)

const headerPriority = "X-Priority"

// rejectClientSuppliedPriority rejects requests that arrive with a
// client-supplied X-Priority header.
// Registered on the LLM route group, not globally. The check fires on header presence
// rather than value so a present-but-empty X-Priority (which Header.Get cannot
// distinguish from absent) is rejected too.
func rejectClientSuppliedPriority(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ec echo.Context) error {
		if len(ec.Request().Header.Values(headerPriority)) > 0 {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("%s is a reserved header and cannot be set by the client", headerPriority),
			)
		}
		return next(ec)
	}
}
