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

import { Anchor, Tag } from "@nvidia/foundations-react-core";
import { createLazyRoute, Link } from "@tanstack/react-router";
import { createColumnHelper } from "@tanstack/react-table";
import { DataTable } from "~/components/DataTable";
import { OverflowGroup } from "~/components/OverflowGroup";
import { useGetClustersSuspense } from "~/generated/api/clusters/clusters";
import type { GetClusterResponse } from "~/generated/model/getClusterResponse";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { useTableUrlSync } from "~/hooks/useTableUrlSync";
import { LIST_REFETCH_INTERVAL } from "~/lib/queryClient";
import { formatDateTime } from "~/utils/formatters";
import { ClustersPageHeading } from "./components/ClustersPageHeading";
import { getStatusLabel, StatusBadge } from "./components/StatusBadge";
import { clusterStatuses } from "./constants";

export const ClustersListRoute = createLazyRoute("/clusters")({
	component: ClustersList,
});

const columnHelper = createColumnHelper<GetClusterResponse>();

const columns = [
	columnHelper.accessor("clusterName", {
		header: "Name",
		cell: ({ row, getValue }) => (
			<Anchor asChild>
				<Link
					params={{ clusterId: row.original.clusterId ?? "" }}
					to="/clusters/$clusterId"
					viewTransition
				>
					{getValue() ?? row.original.clusterId ?? "—"}
				</Link>
			</Anchor>
		),
	}),
	columnHelper.accessor(
		(cluster) =>
			getStatusLabel(cluster.status || GetClusterResponseStatus.NOT_READY),
		{
			id: "status",
			cell: ({ row }) => <StatusBadge status={row.original.status} />,
			header: "Status",
			filterFn: "multiSelect",
			meta: {
				sortType: "text",
				filterVariant: "multi-select",
				filterOptions: clusterStatuses.map((s) => ({
					label: getStatusLabel(s),
					value: getStatusLabel(s),
				})),
			},
		},
	),
	columnHelper.accessor("region", {
		header: "Region",
	}),
	columnHelper.accessor(
		(cluster) => [...new Set(cluster.gpus?.flatMap((gpu) => gpu.name))],
		{
			header: "GPU Types",
			id: "gpuTypes",
			cell: ({ getValue }) => (
				<OverflowGroup kind="popover">
					{getValue().map((gpu) => (
						<Tag color="gray" key={gpu} kind="solid" readOnly>
							{gpu}
						</Tag>
					))}
				</OverflowGroup>
			),
			enableSorting: false,
		},
	),
	columnHelper.accessor("nvcaVersion", {
		header: "Agent Version",
	}),
	columnHelper.accessor("nvcaLastConnected", {
		id: "lastUpdated",
		header: "Last Updated",
		meta: { sortType: "date" },
		cell: ({ getValue }) => formatDateTime(getValue()),
	}),
];

function ClustersList() {
	const { ncaId } = ClustersListRoute.useLoaderData();
	const { data: clusters } = useGetClustersSuspense(ncaId, undefined, {
		query: { refetchInterval: LIST_REFETCH_INTERVAL },
	});
	const search = ClustersListRoute.useSearch();
	const navigate = ClustersListRoute.useNavigate();
	const tableUrlState = useTableUrlSync(search, navigate);
	return (
		<div className="flex flex-col gap-4">
			<ClustersPageHeading />
			<DataTable
				columns={columns}
				data={clusters ?? []}
				reactTableOptions={tableUrlState}
			>
				<DataTable.Toolbar>
					<DataTable.Filters />
					<DataTable.ItemCount label="Cluster" />
					<DataTable.Search placeholder="Search clusters..." />
					<DataTable.ColumnVisibility />
				</DataTable.Toolbar>
				<DataTable.ActiveFilters />
				<DataTable.Content />
				<DataTable.Pagination />
			</DataTable>
		</div>
	);
}
