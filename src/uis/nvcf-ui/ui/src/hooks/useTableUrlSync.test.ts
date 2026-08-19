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

import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BaseTableSearch } from "~/components/DataTable/utils/tableSearchSchema";
import { useTableUrlSync } from "./useTableUrlSync";

const base: BaseTableSearch = {
	page: 1,
	pageSize: 10,
	search: "",
	sort: [],
	filters: [],
};

const navigate = vi.fn();

// Calls the search updater navigate received with the given state,
// returning what the new URL search params would be.
function navigatedSearch(current = base): BaseTableSearch {
	const call = navigate.mock.lastCall;
	if (!call) throw new Error("navigate was not called");
	return call[0].search(current);
}

beforeEach(() => navigate.mockClear());

describe("useTableUrlSync", () => {
	it("converts 1-indexed URL page to 0-indexed table pageIndex", () => {
		const { result } = renderHook(() =>
			useTableUrlSync({ ...base, page: 3 }, navigate),
		);
		expect(result.current.state.pagination).toMatchObject({ pageIndex: 2 });
	});

	it("state.sorting reflects URL sort", () => {
		const sort = [{ id: "createdAt", desc: true }];
		const { result } = renderHook(() =>
			useTableUrlSync({ ...base, sort }, navigate),
		);
		expect(result.current.state.sorting).toEqual(sort);
	});

	it("state.globalFilter reflects URL search", () => {
		const { result } = renderHook(() =>
			useTableUrlSync({ ...base, search: "hello" }, navigate),
		);
		expect(result.current.state.globalFilter).toBe("hello");
	});

	it("onGlobalFilterChange writes to search", () => {
		const { result } = renderHook(() => useTableUrlSync(base, navigate));
		result.current.onGlobalFilterChange("foo");
		expect(navigatedSearch().search).toBe("foo");
	});

	it("onSortingChange writes to sort", () => {
		const { result } = renderHook(() => useTableUrlSync(base, navigate));
		const sort = [{ id: "name", desc: false }];
		result.current.onSortingChange(sort);
		expect(navigatedSearch().sort).toEqual(sort);
	});

	it("onColumnFiltersChange writes to filters", () => {
		const { result } = renderHook(() => useTableUrlSync(base, navigate));
		result.current.onColumnFiltersChange([{ id: "status", value: ["ACTIVE"] }]);
		expect(navigatedSearch().filters).toEqual([
			{ id: "status", value: ["ACTIVE"] },
		]);
	});

	it("onPaginationChange writes to page and pageSize", () => {
		const { result } = renderHook(() => useTableUrlSync(base, navigate));
		result.current.onPaginationChange({ pageIndex: 2, pageSize: 25 });
		expect(navigatedSearch()).toMatchObject({ page: 3, pageSize: 25 });
	});
});
