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
import { ListFilter } from "lucide-react";
import { useDataTable } from "./DataTableContext";
import { buildFilterItems, getColumnLabel } from "./utils";

export function Filters() {
	const table = useDataTable("Filters");

	const filterableColumns = table
		.getAllColumns()
		.filter((col) => col.columnDef.meta?.filterVariant);

	if (filterableColumns.length === 0) return null;

	const items: DropdownEntry[] = filterableColumns.map((col) => ({
		kind: "sub",
		children: getColumnLabel(col),
		items: buildFilterItems(col, "filter"),
	}));

	return (
		<Dropdown items={items}>
			<ListFilter size="1em" />
			Filter
		</Dropdown>
	);
}
