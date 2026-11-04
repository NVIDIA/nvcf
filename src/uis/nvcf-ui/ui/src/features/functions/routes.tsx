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
import { baseTableSearchSchema } from "~/components/DataTable/utils/tableSearchSchema";
import { getActiveNcaId } from "~/features/accounts/getActiveNcaId";
import {
	getGetAllFunctionDeploymentsQueryOptions,
	getGetFunctionDeploymentQueryOptions,
} from "~/generated/api/function-deployment/function-deployment";
import {
	getGetAllFunctionsQueryOptions,
	getGetFunctionVersionQueryOptions,
} from "~/generated/api/function-management/function-management";
import { rootRoute } from "~/rootRoute";
import { DEPLOYED_FUNCTION_STATUSES } from "./constants";
import { FunctionsListPending } from "./FunctionsListPending";

const functionsSearchSchema = baseTableSearchSchema.extend({
	sort: baseTableSearchSchema.shape.sort.default([
		{ id: "createdAt", desc: true },
	]),
});

export const functionsListRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "functions",
	head: () => ({ meta: [{ title: "Functions · NVCF" }] }),
	validateSearch: functionsSearchSchema,
	pendingComponent: FunctionsListPending,
	loader: async ({ context: { queryClient } }) => {
		const ncaId = await getActiveNcaId(queryClient);
		await Promise.all([
			queryClient.ensureQueryData({
				...getGetAllFunctionsQueryOptions(ncaId),
				revalidateIfStale: true,
			}),
			queryClient.ensureQueryData({
				...getGetAllFunctionDeploymentsQueryOptions(ncaId),
				revalidateIfStale: true,
			}),
		]);
		return { ncaId };
	},
}).lazy(() => import("./FunctionsList").then((m) => m.FunctionListRoute));

export const functionDetailRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "functions/$functionId/versions/$versionId",
	staticData: { account: { redirectOnChange: "/functions" } },
	loader: async ({ context: { queryClient }, params }) => {
		const ncaId = await getActiveNcaId(queryClient);
		const data = await queryClient.ensureQueryData({
			...getGetFunctionVersionQueryOptions(
				ncaId,
				params.functionId,
				params.versionId,
			),
			revalidateIfStale: true,
		});
		const fn = data?.function;
		if (fn && DEPLOYED_FUNCTION_STATUSES.includes(fn.status)) {
			void queryClient.prefetchQuery(
				getGetFunctionDeploymentQueryOptions(
					ncaId,
					params.functionId,
					params.versionId,
				),
			);
		}
		return { ncaId, functionName: fn?.name };
	},
	head: ({ loaderData }) => ({
		meta: [{ title: `${loaderData?.functionName ?? "Function"} · NVCF` }],
	}),
}).lazy(() => import("./FunctionDetail").then((m) => m.FunctionDetailRoute));
