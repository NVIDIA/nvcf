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

import { Button, Dropdown, Group, Tag } from "@nvidia/foundations-react-core";
import { X } from "lucide-react";
import { useDataTable } from "./DataTableContext";
import { buildFilterItems, getColumnLabel } from "./utils";

export function ActiveFilters() {
	const table = useDataTable("ActiveFilters");
	const columnFilters = table.getState().columnFilters;

	if (columnFilters.length === 0) return null;

	const activeFilters = columnFilters.flatMap((columnFilter) => {
		const column = table.getColumn(columnFilter.id);
		if (!column) return [];

		const { filterVariant, filterOptions = [] } = column.columnDef.meta ?? {};
		if (!filterVariant) return [];

		const columnName = getColumnLabel(column);

		const resolveLabel = (value: string) =>
			filterOptions.find((o) => o.value === value)?.label ?? value;

		const displayValue = Array.isArray(columnFilter.value)
			? (columnFilter.value as string[]).map(resolveLabel).join(", ")
			: resolveLabel(columnFilter.value as string);

		return [{ id: columnFilter.id, column, columnName, displayValue }];
	});

	return (
		<div className="flex flex-wrap items-center gap-2">
			{activeFilters.map((activeFilter) => (
				<Group key={activeFilter.id}>
					<Dropdown
						asChild
						items={buildFilterItems(activeFilter.column, "active-filter")}
					>
						<Tag className="whitespace-nowrap" color="gray" kind="outline">
							<b>{activeFilter.columnName}: </b>
							{activeFilter.displayValue}
						</Tag>
					</Dropdown>
					<Tag
						aria-label="Clear filter"
						color="gray"
						kind="outline"
						onClick={() => activeFilter.column.setFilterValue(undefined)}
					>
						<X size="1em" />
					</Tag>
				</Group>
			))}
			<Button kind="tertiary" onClick={() => table.resetColumnFilters()}>
				<X size="1em" />
				Clear Filters
			</Button>
		</div>
	);
}
