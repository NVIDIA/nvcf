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
import { playgroundRoute } from "./features/playground/routes";
import { recipeRoute, recipesRoute } from "./features/recipes/routes";
import { indexRoute, registryRoute } from "./features/registry/routes";
import { queryClient } from "./lib/queryClient";
import { parseSearch, stringifySearch } from "./lib/search";
import { rootRoute } from "./rootRoute";

const routeTree = rootRoute.addChildren([
	indexRoute,
	registryRoute,
	recipesRoute,
	recipeRoute,
	playgroundRoute,
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
	parseSearch,
	stringifySearch,
	context: { queryClient },
});

declare module "@tanstack/react-router" {
	interface Register {
		router: typeof router;
	}
}
