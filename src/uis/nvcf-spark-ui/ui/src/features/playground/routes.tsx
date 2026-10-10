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

import { createRoute } from "@tanstack/react-router";
import { StackError } from "~/features/registry/components/StackError";
import { getGetRegistryQueryOptions } from "~/generated/api/registry/registry";
import { validateModelSearch } from "~/lib/search";
import { rootRoute } from "~/rootRoute";

/**
 * Chat with one model (`?model=`). It has no nav item: an endpoint's "Try in
 * playground" opens it in a new tab, so every tab is one model.
 */
export const playgroundRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "playground",
	validateSearch: validateModelSearch,
	head: ({ match }) => ({
		meta: [
			{
				title: match.search.model
					? `${match.search.model} · Playground`
					: "Playground · NVCF Gateway",
			},
		],
	}),
	errorComponent: StackError,
	loader: ({ context: { queryClient } }) =>
		queryClient.ensureQueryData({
			...getGetRegistryQueryOptions(),
			revalidateIfStale: true,
		}),
}).lazy(() => import("./Playground").then((m) => m.PlaygroundRoute));
