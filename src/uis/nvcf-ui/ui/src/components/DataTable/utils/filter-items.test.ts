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

import type { Column, ColumnMeta, RowData } from "@tanstack/react-table";
import { describe, expect, it, vi } from "vitest";
import { buildFilterItems } from "./filter-items";

const options = [
	{ label: "Active", value: "active" },
	{ label: "Inactive", value: "inactive" },
];

function mockColumn(
	filterVariant?: ColumnMeta<RowData, unknown>["filterVariant"],
	filterValue?: unknown,
) {
	return {
		id: "status",
		columnDef: { meta: { filterVariant, filterOptions: options } },
		getFilterValue: () => filterValue,
		setFilterValue: vi.fn(),
	} as unknown as Column<RowData, unknown>;
}

describe("buildFilterItems", () => {
	it("returns empty array for unknown filter variant", () => {
		expect(buildFilterItems(mockColumn(undefined), "test")).toEqual([]);
	});

	describe("multi-select", () => {
		it("returns one item per option", () => {
			const items = buildFilterItems(mockColumn("multi-select"), "test");
			expect(items).toHaveLength(options.length);
		});

		it("reflects selected state from current filter value", () => {
			const items = buildFilterItems(
				mockColumn("multi-select", ["active"]),
				"test",
			);
			expect(items.map((i) => "checked" in i && i.checked)).toEqual([
				true,
				false,
			]);
		});

		it("adds a value when checked", () => {
			const column = mockColumn("multi-select", ["active"]);
			const item = buildFilterItems(column, "test")[1];
			expect(item.kind).toBe("checkbox");
			if (item.kind === "checkbox") item.onCheckedChange?.(true);
			expect(column.setFilterValue).toHaveBeenCalledWith([
				"active",
				"inactive",
			]);
		});

		it("clears filter when last value is unchecked", () => {
			const column = mockColumn("multi-select", ["active"]);
			const item = buildFilterItems(column, "test")[0];
			expect(item.kind).toBe("checkbox");
			if (item.kind === "checkbox") item.onCheckedChange?.(false);
			expect(column.setFilterValue).toHaveBeenCalledWith(undefined);
		});
	});

	describe("single-select", () => {
		it("returns a single radio group", () => {
			const items = buildFilterItems(mockColumn("single-select"), "test");
			expect(items).toHaveLength(1);
			expect(items[0].kind).toBe("radio");
		});

		it("scopes radio name with prefix and column id", () => {
			const items = buildFilterItems(mockColumn("single-select"), "filter");
			expect("name" in items[0] && items[0].name).toBe("filter-status");
		});

		it("sets filter on value change", () => {
			const column = mockColumn("single-select");
			const item = buildFilterItems(column, "test")[0];
			expect(item.kind).toBe("radio");
			if (item.kind !== "radio") return;
			item.onValueChange?.("inactive");
			expect(column.setFilterValue).toHaveBeenCalledWith("inactive");
		});

		it("clears filter when value is empty", () => {
			const column = mockColumn("single-select", "active");
			const item = buildFilterItems(column, "test")[0];
			expect(item.kind).toBe("radio");
			if (item.kind !== "radio") return;
			item.onValueChange?.("");
			expect(column.setFilterValue).toHaveBeenCalledWith(undefined);
		});
	});
});
