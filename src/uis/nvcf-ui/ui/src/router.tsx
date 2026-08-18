/**
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { createRouter } from "@tanstack/react-router";
import { NotFound } from "./components/NotFound";
import { RouteErrorFallback } from "./components/RouteErrorFallback";
import { RouteSpinner } from "./components/RouteSpinner";
import {
	clusterDetailRoute,
	clustersListRoute,
} from "./features/clusters/routes";
import { dashboardRoute } from "./features/dashboard/routes";
import {
	functionDetailRoute,
	functionsListRoute,
} from "./features/functions/routes";
import { queryClient } from "./lib/queryClient";
import { rootRoute } from "./rootRoute";

const routeTree = rootRoute.addChildren([
	clustersListRoute,
	clusterDetailRoute,
	dashboardRoute,
	functionsListRoute,
	functionDetailRoute,
]);

export const router = createRouter({
	routeTree,
	basepath: import.meta.env.BASE_URL,
	defaultPreload: "intent",
	defaultPendingMs: 300,
	defaultPreloadStaleTime: 0,
	defaultErrorComponent: RouteErrorFallback,
	defaultNotFoundComponent: NotFound,
	defaultPendingComponent: RouteSpinner,
	scrollRestoration: true,
	context: { queryClient },
});

declare module "@tanstack/react-router" {
	interface Register {
		router: typeof router;
	}

	interface StaticDataRouteOption {
		account?: {
			/**
			 * Where to send the user if they switch the active account while on
			 * this route. Set on account-specific pages (e.g. a function detail)
			 * that won't resolve under a different account; omit on pages that
			 * should just refresh in place.
			 */
			redirectOnChange?: string;
		};
	}
}
