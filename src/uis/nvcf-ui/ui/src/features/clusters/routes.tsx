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
	getGetClusterForNcaIdAndClusterIdQueryOptions,
	getGetClustersQueryOptions,
} from "~/generated/api/clusters/clusters";
import { rootRoute } from "~/rootRoute";
import { ClustersListPending } from "./ClustersListPending";

const clustersSearchSchema = baseTableSearchSchema.extend({
	sort: baseTableSearchSchema.shape.sort.default([
		{ id: "lastUpdated", desc: true },
	]),
});

export const clustersListRoute = createRoute({
	path: "clusters",
	getParentRoute: () => rootRoute,
	head: () => ({ meta: [{ title: "Compute Clusters · NVCF" }] }),
	validateSearch: clustersSearchSchema,
	pendingComponent: ClustersListPending,
	loader: async ({ context: { queryClient } }) => {
		const ncaId = await getActiveNcaId(queryClient);
		await queryClient.ensureQueryData({
			...getGetClustersQueryOptions(ncaId),
			revalidateIfStale: true,
		});
		return { ncaId };
	},
}).lazy(() => import("./ClustersList").then((m) => m.ClustersListRoute));

export const clusterDetailRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "clusters/$clusterId",
	staticData: { account: { redirectOnChange: "/clusters" } },
	loader: async ({ context: { queryClient }, params }) => {
		const ncaId = await getActiveNcaId(queryClient);
		const cluster = await queryClient.ensureQueryData({
			...getGetClusterForNcaIdAndClusterIdQueryOptions(ncaId, params.clusterId),
			revalidateIfStale: true,
		});
		return { ncaId, clusterName: cluster.clusterName };
	},
	head: ({ loaderData }) => ({
		meta: [{ title: `${loaderData?.clusterName ?? "Cluster"} · NVCF` }],
	}),
}).lazy(() => import("./ClusterDetails").then((m) => m.ClusterDetailsRoute));
