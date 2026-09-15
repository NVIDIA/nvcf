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
	PaginationArrowButton,
	PaginationControlsGroup,
	PaginationDivider,
	PaginationNavigationGroup,
	PaginationPageCountText,
	PaginationPageInput,
	PaginationPageList,
	PaginationPageSizeSelect,
	PaginationRoot,
} from "@nvidia/foundations-react-core";
import { useEffect } from "react";
import { useDataTable } from "./DataTableContext";

interface PaginationProps {
	pageSizeOptions?: number[];
}

export function Pagination({
	pageSizeOptions = [10, 25, 50, 100],
}: PaginationProps) {
	const table = useDataTable("Pagination");
	const { pageIndex, pageSize } = table.getState().pagination;
	const totalRows = table.getRowCount();
	const lastPageIndex = table.getPageCount() - 1;

	useEffect(() => {
		if (lastPageIndex >= 0 && pageIndex > lastPageIndex) {
			table.setPageIndex(lastPageIndex);
		}
	}, [pageIndex, lastPageIndex, table]);

	return (
		<PaginationRoot
			className="nv-pagination nv-pagination--kind-tabs"
			onPageChange={(page: number) => table.setPageIndex(page - 1)}
			onPageSizeChange={(size: number) => {
				table.setPageSize(size);
				table.setPageIndex(0);
			}}
			page={pageIndex + 1}
			pageSize={pageSize}
			pageSizeOptions={pageSizeOptions}
			totalItems={totalRows}
		>
			<PaginationControlsGroup side="start">
				Items per page
				<PaginationPageSizeSelect />
				<PaginationDivider />
			</PaginationControlsGroup>
			<PaginationNavigationGroup withTabs>
				<PaginationArrowButton direction="first" />
				<PaginationArrowButton direction="previous" />
				<PaginationPageList />
				<PaginationArrowButton direction="next" />
				<PaginationArrowButton direction="last" />
			</PaginationNavigationGroup>
			<PaginationControlsGroup side="end">
				<PaginationDivider />
				<PaginationPageInput />
				<PaginationPageCountText
					pageCountTextFormatFn={({ total }) =>
						`of ${total} ${total === 1 ? "page" : "pages"}`
					}
				/>
			</PaginationControlsGroup>
		</PaginationRoot>
	);
}
