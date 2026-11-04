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

import type {
	DropdownCheckboxItemEntry,
	DropdownRadioGroupEntry,
} from "@nvidia/foundations-react-core";
import type { Column, RowData } from "@tanstack/react-table";

export function buildFilterItems(
	column: Column<RowData, unknown>,
	namePrefix: string,
): (DropdownCheckboxItemEntry | DropdownRadioGroupEntry)[] {
	const { filterVariant, filterOptions = [] } = column.columnDef.meta ?? {};

	if (filterVariant === "multi-select") {
		const current = column.getFilterValue();
		const selected = Array.isArray(current) ? (current as string[]) : [];
		return filterOptions.map((opt) => ({
			kind: "checkbox" as const,
			children: opt.label,
			checked: selected.includes(opt.value),
			onCheckedChange: (checked: boolean | "indeterminate") => {
				const next = checked
					? [...selected, opt.value]
					: selected.filter((v) => v !== opt.value);
				column.setFilterValue(next.length > 0 ? next : undefined);
			},
		}));
	}

	if (filterVariant === "single-select") {
		const currentFilter = column.getFilterValue();
		return [
			{
				kind: "radio" as const,
				slotHeading: null,
				name: `${namePrefix}-${column.id}`,
				value: (currentFilter as string) ?? "",
				onValueChange: (value: string) => {
					column.setFilterValue(value || undefined);
				},
				items: filterOptions.map((opt) => ({
					children: opt.label,
					value: opt.value,
				})),
			},
		];
	}

	return [];
}
