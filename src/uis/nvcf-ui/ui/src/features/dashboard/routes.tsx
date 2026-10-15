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
import { getActiveNcaId } from "~/features/accounts/getActiveNcaId";
import { getGetClustersQueryOptions } from "~/generated/api/clusters/clusters";
import { getGetControlPlaneStatusQueryOptions } from "~/generated/api/control-plane/control-plane";
import { getGetAllFunctionDeploymentsQueryOptions } from "~/generated/api/function-deployment/function-deployment";
import { getGetAllFunctionsQueryOptions } from "~/generated/api/function-management/function-management";
import { rootRoute } from "~/rootRoute";

export const dashboardRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "/",
	head: () => ({ meta: [{ title: "Dashboard · NVCF" }] }),
	loader: async ({ context: { queryClient } }) => {
		const ncaId = await getActiveNcaId(queryClient);

		// Fire-and-forget: warm the cache so panels resolve faster, but don't
		// block navigation — each panel's Suspense boundary handles its own state.
		void queryClient.ensureQueryData({
			...getGetClustersQueryOptions(ncaId),
			revalidateIfStale: true,
		});
		void queryClient.ensureQueryData({
			...getGetControlPlaneStatusQueryOptions(),
			revalidateIfStale: true,
		});
		void queryClient.ensureQueryData({
			...getGetAllFunctionsQueryOptions(ncaId),
			revalidateIfStale: true,
		});
		void queryClient.ensureQueryData({
			...getGetAllFunctionDeploymentsQueryOptions(ncaId),
			revalidateIfStale: true,
		});

		return { ncaId };
	},
}).lazy(() => import("./Dashboard").then((d) => d.Route));
