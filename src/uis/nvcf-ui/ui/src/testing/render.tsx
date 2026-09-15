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

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AnyRoute } from "@tanstack/react-router";
import {
	createMemoryHistory,
	createRouter,
	RouterProvider,
} from "@tanstack/react-router";
import { type RenderOptions, render } from "@testing-library/react";
import { NotFound } from "~/components/NotFound";
import { RouteErrorFallback } from "~/components/RouteErrorFallback";
import { RouteSpinner } from "~/components/RouteSpinner";
import { rootRoute } from "~/rootRoute";

function createTestQueryClient() {
	return new QueryClient({
		defaultOptions: {
			queries: { retry: false },
		},
	});
}

interface RenderWithRouterOptions extends Omit<RenderOptions, "wrapper"> {
	initialLocation?: string;
	routes?: AnyRoute[];
}

export function renderWithRouter({
	initialLocation = "/",
	routes = [],
	...renderOptions
}: RenderWithRouterOptions = {}) {
	if (routes.length === 0) {
		throw new Error("At least one route is required.");
	}

	const queryClient = createTestQueryClient();
	const routeTree = rootRoute.addChildren(routes);
	const router = createRouter({
		routeTree,
		history: createMemoryHistory({ initialEntries: [initialLocation] }),
		context: { queryClient },
		defaultErrorComponent: RouteErrorFallback,
		defaultNotFoundComponent: NotFound,
		defaultPendingComponent: RouteSpinner,
	});

	return {
		...render(
			<QueryClientProvider client={queryClient}>
				<RouterProvider router={router} />
			</QueryClientProvider>,
			renderOptions,
		),
		router,
		queryClient,
	};
}
