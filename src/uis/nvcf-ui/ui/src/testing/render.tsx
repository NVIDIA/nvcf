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
import type { ReactElement } from "react";
import { RouteErrorFallback } from "~/components/RouteErrorFallback";
import { routeTree as appRouteTree } from "~/routeTree.gen";

function createTestQueryClient() {
	return new QueryClient({
		defaultOptions: {
			queries: { retry: false },
		},
	});
}

/** Renders a component that reads its own queries but uses no router hooks. */
export function renderWithQueryClient(
	ui: ReactElement,
	renderOptions?: Omit<RenderOptions, "wrapper">,
) {
	const queryClient = createTestQueryClient();

	return {
		...render(
			<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
			renderOptions,
		),
		queryClient,
	};
}

interface RenderWithRouterOptions extends Omit<RenderOptions, "wrapper"> {
	initialLocation?: string;
	/**
	 * Defaults to the app's generated route tree. Pass a test-local tree to
	 * render a component that uses router hooks without the app's pages.
	 */
	routeTree?: AnyRoute;
}

export function renderWithRouter({
	initialLocation = "/",
	routeTree = appRouteTree,
	...renderOptions
}: RenderWithRouterOptions = {}) {
	const queryClient = createTestQueryClient();
	const router = createRouter({
		routeTree,
		history: createMemoryHistory({ initialEntries: [initialLocation] }),
		context: { queryClient },
		// Same as src/router.tsx, so pages render their real error state.
		defaultErrorComponent: RouteErrorFallback,
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
