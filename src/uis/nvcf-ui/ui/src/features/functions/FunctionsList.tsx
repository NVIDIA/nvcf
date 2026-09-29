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

import { createLazyRoute } from "@tanstack/react-router";
import type { ColumnDef } from "@tanstack/react-table";
import { DataTable } from "~/components/DataTable";
import { useTableUrlSync } from "~/hooks/useTableUrlSync";
import { LIST_REFETCH_INTERVAL } from "~/lib/queryClient";
import { toTitleCase } from "~/utils/formatters";
import { FunctionCard } from "./components/FunctionCard";
import { FunctionsPageHeading } from "./components/FunctionsPageHeading";
import { FUNCTION_TYPE, functionStatusValues } from "./constants";
import { useFunctionsWithDeployments } from "./hooks/useFunctionsWithDeployments";
import type { FunctionWithDeployment } from "./types";

export const FunctionListRoute = createLazyRoute("/functions")({
	component: FunctionsList,
});

const columns: ColumnDef<FunctionWithDeployment, unknown>[] = [
	{
		accessorKey: "name",
		header: "Name",
		meta: { sortType: "text" },
	},
	{
		accessorKey: "id",
		enableSorting: false,
	},
	{
		accessorKey: "status",
		header: "Status",
		enableSorting: false,
		filterFn: "multiSelect",
		meta: {
			filterVariant: "multi-select",
			filterOptions: functionStatusValues.map((s) => ({
				label: toTitleCase(s),
				value: s,
			})),
		},
	},
	{
		accessorFn: (row) =>
			row.helmChart ? FUNCTION_TYPE.HELM : FUNCTION_TYPE.CONTAINER,
		header: "Function Type",
		enableSorting: false,
		meta: {
			filterVariant: "single-select",
			filterOptions: Object.values(FUNCTION_TYPE).map((t) => ({
				label: t,
				value: t,
			})),
		},
	},
	{
		id: "instanceTypes",
		accessorFn: (row) =>
			row.deploymentSpecifications.map((s) => s.instanceType).filter(Boolean),
		header: "Instance Types",
		enableSorting: false,
	},
	{
		accessorKey: "tags",
		enableSorting: false,
	},
	{
		accessorKey: "createdAt",
		header: "Created",
		meta: { sortType: "date" },
	},
];

function FunctionsList() {
	const { ncaId } = FunctionListRoute.useLoaderData();
	const { functions } = useFunctionsWithDeployments(ncaId, undefined, {
		refetchInterval: LIST_REFETCH_INTERVAL,
	});
	const search = FunctionListRoute.useSearch();
	const navigate = FunctionListRoute.useNavigate();
	const tableUrlState = useTableUrlSync(search, navigate);

	return (
		<div className="flex flex-col gap-4">
			<FunctionsPageHeading />
			<DataTable
				columns={columns}
				data={functions}
				reactTableOptions={tableUrlState}
			>
				<DataTable.Toolbar>
					<DataTable.Filters />
					<DataTable.ItemCount label="Function" />
					<DataTable.Search placeholder="Search functions..." />
					<DataTable.Sort />
				</DataTable.Toolbar>
				<DataTable.ActiveFilters />
				<DataTable.Content<FunctionWithDeployment>
					renderContent={({ rows }) => (
						<div className="flex flex-col gap-6">
							{rows.map((row) => (
								<FunctionCard fn={row.original} key={row.id} />
							))}
						</div>
					)}
				/>
				<DataTable.Pagination />
			</DataTable>
		</div>
	);
}
