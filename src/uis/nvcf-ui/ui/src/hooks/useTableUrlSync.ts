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
	ColumnFiltersState,
	OnChangeFn,
	PaginationState,
	SortingState,
	Updater,
} from "@tanstack/react-table";
import { useCallback } from "react";
import type { BaseTableSearch } from "~/components/DataTable/utils/tableSearchSchema";

function resolve<T>(updater: Updater<T>, current: T): T {
	return typeof updater === "function"
		? (updater as (v: T) => T)(current)
		: updater;
}

type NavigateFn<TSearch extends BaseTableSearch> = (opts: {
	search: (prev: TSearch) => TSearch;
	replace?: boolean;
}) => void;

export type TableUrlSyncResult = {
	autoResetPageIndex: false;
	state: {
		pagination: PaginationState;
		globalFilter: string;
		sorting: SortingState;
		columnFilters: ColumnFiltersState;
	};
	onPaginationChange: OnChangeFn<PaginationState>;
	onGlobalFilterChange: OnChangeFn<string>;
	onSortingChange: OnChangeFn<SortingState>;
	onColumnFiltersChange: OnChangeFn<ColumnFiltersState>;
};

export function useTableUrlSync<TSearch extends BaseTableSearch>(
	search: TSearch,
	navigate: NavigateFn<TSearch>,
): TableUrlSyncResult {
	const onPaginationChange: OnChangeFn<PaginationState> = useCallback(
		(updater) => {
			navigate({
				search: (old) => {
					const prev: PaginationState = {
						pageIndex: old.page - 1,
						pageSize: old.pageSize,
					};
					const next = resolve(updater, prev);
					return { ...old, page: next.pageIndex + 1, pageSize: next.pageSize };
				},
				replace: true,
			});
		},
		[navigate],
	);

	const onGlobalFilterChange: OnChangeFn<string> = useCallback(
		(updater) => {
			navigate({
				search: (old) => {
					const next = resolve(updater, old.search);
					return { ...old, search: next, page: 1 };
				},
				replace: true,
			});
		},
		[navigate],
	);

	const onSortingChange: OnChangeFn<SortingState> = useCallback(
		(updater) => {
			navigate({
				search: (old) => {
					const next = resolve(updater, old.sort);
					return { ...old, sort: next, page: 1 };
				},
				replace: true,
			});
		},
		[navigate],
	);

	const onColumnFiltersChange: OnChangeFn<ColumnFiltersState> = useCallback(
		(updater) => {
			navigate({
				search: (old) => {
					const next = resolve(updater, old.filters);
					return { ...old, filters: next, page: 1 };
				},
				replace: true,
			});
		},
		[navigate],
	);

	return {
		autoResetPageIndex: false,
		state: {
			pagination: {
				pageIndex: search.page - 1,
				pageSize: search.pageSize,
			},
			globalFilter: search.search,
			sorting: search.sort,
			columnFilters: search.filters,
		},
		onPaginationChange,
		onGlobalFilterChange,
		onSortingChange,
		onColumnFiltersChange,
	};
}
