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

import { Dropdown, type DropdownEntry } from "@nvidia/foundations-react-core";
import type { Column, RowData } from "@tanstack/react-table";
import {
	ArrowDownWideNarrow,
	ArrowUpDown,
	ArrowUpNarrowWide,
} from "lucide-react";
import { useDataTable } from "./DataTableContext";
import { getColumnLabel } from "./utils";

const sortLabelPresets = {
	text: { asc: "A-Z", desc: "Z-A" },
	date: { asc: "Oldest First", desc: "Newest First" },
	number: { asc: "Low to High", desc: "High to Low" },
};

function getSortLabels(column: Column<RowData, unknown>) {
	const meta = column.columnDef.meta;
	if (meta?.sortLabels) return meta.sortLabels;
	return sortLabelPresets[meta?.sortType ?? "text"];
}

export function Sort() {
	const table = useDataTable("Sort");
	const sorting = table.getState().sorting;
	const activeSort = sorting[0];

	const sortableColumns = table
		.getAllColumns()
		.filter((col) => col.getCanSort());

	if (sortableColumns.length === 0) return null;

	const TriggerIcon = activeSort
		? activeSort.desc
			? ArrowDownWideNarrow
			: ArrowUpNarrowWide
		: ArrowUpDown;

	const activeColumn = activeSort ? table.getColumn(activeSort.id) : undefined;
	const triggerLabel = activeColumn
		? `Sort: ${getColumnLabel(activeColumn)} (${getSortLabels(activeColumn)[activeSort.desc ? "desc" : "asc"]})`
		: "Sort";

	const value = activeSort
		? `${activeSort.id}:${activeSort.desc ? "desc" : "asc"}`
		: "";

	const items: DropdownEntry[] = [
		{
			kind: "radio",
			slotHeading: null,
			name: "sort",
			radioKind: "check",
			value,
			onValueChange: (next: string) => {
				const [id, dir] = next.split(":");
				table.setSorting([{ id, desc: dir === "desc" }]);
			},
			items: sortableColumns.flatMap((col) => {
				const columnName = getColumnLabel(col);
				const sortLabels = getSortLabels(col);
				return [
					{
						children: `${columnName} (${sortLabels.asc})`,
						value: `${col.id}:asc`,
					},
					{
						children: `${columnName} (${sortLabels.desc})`,
						value: `${col.id}:desc`,
					},
				];
			}),
		},
	];

	return (
		<Dropdown items={items}>
			<TriggerIcon size="1em" />
			{triggerLabel}
		</Dropdown>
	);
}
