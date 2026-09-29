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
	Dropdown,
	type DropdownCheckboxItemEntry,
} from "@nvidia/foundations-react-core";
import { Columns3 } from "lucide-react";
import { useDataTable } from "./DataTableContext";
import { getColumnLabel } from "./utils";

export function ColumnVisibility() {
	const table = useDataTable("ColumnVisibility");

	const hideableColumns = table
		.getAllColumns()
		.filter((col) => col.getCanHide());

	if (hideableColumns.length === 0) return null;

	const items: DropdownCheckboxItemEntry[] = hideableColumns.map((col) => ({
		kind: "checkbox",
		children: getColumnLabel(col),
		checked: col.getIsVisible(),
		onCheckedChange: (checked) => col.toggleVisibility(!!checked),
	}));

	return (
		<Dropdown items={items}>
			<Columns3 size="1em" />
			Columns
		</Dropdown>
	);
}
