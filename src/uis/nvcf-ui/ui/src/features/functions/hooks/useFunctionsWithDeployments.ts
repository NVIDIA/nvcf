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

import type { UseSuspenseQueryResult } from "@tanstack/react-query";
import { useSuspenseQueries } from "@tanstack/react-query";
import {
	type GetAllFunctionDeploymentsSuspenseQueryResult,
	getGetAllFunctionDeploymentsSuspenseQueryOptions,
} from "~/generated/api/function-deployment/function-deployment";
import {
	type GetAllFunctionsSuspenseQueryResult,
	getGetAllFunctionsSuspenseQueryOptions,
} from "~/generated/api/function-management/function-management";
import type { FunctionWithDeployment } from "../types";

function combine([functionsResult, deploymentsResult]: [
	UseSuspenseQueryResult<GetAllFunctionsSuspenseQueryResult, Error>,
	UseSuspenseQueryResult<GetAllFunctionDeploymentsSuspenseQueryResult, Error>,
]): {
	functions: FunctionWithDeployment[];
} {
	const specsMap = new Map(
		deploymentsResult.data?.deployments?.map((d) => [
			d.functionVersionId,
			{ specs: d.deploymentSpecifications, lastUpdatedAt: d.lastUpdatedAt },
		]),
	);
	return {
		functions: (functionsResult.data?.functions ?? []).map((fn) => ({
			...fn,
			deploymentSpecifications: specsMap.get(fn.versionId)?.specs ?? [],
			lastUpdatedAt: specsMap.get(fn.versionId)?.lastUpdatedAt ?? "",
		})),
	};
}

type BaseCombineResult = ReturnType<typeof combine>;

/** Config-only query options safe to apply to both underlying queries. */
type SharedQueryOptions = {
	refetchInterval?: number;
	staleTime?: number;
};

export function useFunctionsWithDeployments<T = BaseCombineResult>(
	ncaId: string,
	select?: (data: BaseCombineResult) => T,
	queryOptions?: SharedQueryOptions,
) {
	return useSuspenseQueries({
		queries: [
			{
				...getGetAllFunctionsSuspenseQueryOptions(ncaId),
				...queryOptions,
			},
			{
				...getGetAllFunctionDeploymentsSuspenseQueryOptions(ncaId),
				...queryOptions,
			},
		],
		combine(results) {
			const base = combine(results);
			return (select ? select(base) : base) as T;
		},
	});
}
