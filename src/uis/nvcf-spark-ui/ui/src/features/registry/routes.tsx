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

import { createRoute, redirect } from "@tanstack/react-router";
import { getGetConfigQueryOptions } from "~/generated/api/config/config";
import { getGetRegistryQueryOptions } from "~/generated/api/registry/registry";
import { validateModelSearch } from "~/lib/search";
import { rootRoute } from "~/rootRoute";
import { StackError } from "./components/StackError";
import { EndpointsListPending } from "./EndpointsListPending";

export const indexRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "/",
	beforeLoad: () => {
		throw redirect({ to: "/registry", replace: true });
	},
});

export const registryRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "registry",
	validateSearch: validateModelSearch,
	head: () => ({ meta: [{ title: "Endpoint registry · NVCF Gateway" }] }),
	pendingComponent: EndpointsListPending,
	errorComponent: StackError,
	loader: async ({ context: { queryClient } }) => {
		await Promise.all([
			queryClient.ensureQueryData({
				...getGetRegistryQueryOptions(),
				revalidateIfStale: true,
			}),
			// Without config the snippets fall back to placeholders, so a failure
			// here doesn't fail the page.
			queryClient
				.ensureQueryData(getGetConfigQueryOptions())
				.catch(() => undefined),
		]);
	},
}).lazy(() => import("./EndpointsList").then((m) => m.EndpointsListRoute));
