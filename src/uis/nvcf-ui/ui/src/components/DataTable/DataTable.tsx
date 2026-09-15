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

import {
	type ColumnDef,
	getCoreRowModel,
	getFilteredRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	type RowData,
	type Table,
	type TableOptions,
	useReactTable,
} from "@tanstack/react-table";
import type { ReactNode } from "react";
import { DataTableProvider } from "./DataTableContext";
import { filterFns } from "./utils";

interface DataTableProps<TData extends RowData> {
	data: TData[];
	// biome-ignore lint/suspicious/noExplicitAny: ColumnDef is invariant in TValue so unknown breaks cell getValue() calls; any is intentional to allow heterogeneous column arrays
	columns: ColumnDef<TData, any>[];
	children: ReactNode;
	reactTableOptions?: Partial<TableOptions<TData>>;
}

export function DataTable<TData extends RowData>({
	data,
	columns,
	children,
	reactTableOptions,
}: DataTableProps<TData>) {
	const table = useReactTable({
		data,
		columns,
		columnResizeMode: "onChange",
		filterFns,
		globalFilterFn: "includesValue",
		getColumnCanGlobalFilter: () => true,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getFilteredRowModel: getFilteredRowModel(),
		...reactTableOptions,
	});

	return (
		<DataTableProvider value={{ table: table as Table<RowData> }}>
			{children}
		</DataTableProvider>
	);
}
