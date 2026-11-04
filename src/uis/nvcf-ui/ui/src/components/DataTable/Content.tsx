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
	Button,
	StatusMessage,
	TableBody,
	TableDataCell,
	TableHead,
	TableHeaderCell,
	TableRoot,
	TableRow,
} from "@nvidia/foundations-react-core";
import {
	flexRender,
	type Header,
	type Row,
	type RowData,
	type SortDirection,
	type Table,
} from "@tanstack/react-table";
import clsx from "clsx";
import {
	ArrowDown,
	ArrowUp,
	ArrowUpDown,
	CircleSlash,
	SearchX,
} from "lucide-react";
import { type CSSProperties, memo, type ReactNode, useMemo } from "react";
import { useDataTable } from "./DataTableContext";

function SortIcon({ sorted }: { sorted: false | SortDirection }) {
	if (sorted === "asc") return <ArrowUp size="1em" />;
	if (sorted === "desc") return <ArrowDown size="1em" />;
	return <ArrowUpDown size="1em" />;
}

function HeaderCell({ header }: { header: Header<unknown, unknown> }) {
	const canSort = header.column.getCanSort();
	const sorted = header.column.getIsSorted();
	const canResize = header.column.getCanResize();

	const content = header.isPlaceholder
		? null
		: flexRender(header.column.columnDef.header, header.getContext());

	return (
		<TableHeaderCell
			className="relative"
			style={{
				width: `calc(var(--header-${header.id}-size) * 1px)`,
			}}
		>
			{canSort ? (
				<Button
					className="-mx-3 -my-2"
					kind="tertiary"
					onClick={header.column.getToggleSortingHandler()}
				>
					{content}
					<SortIcon sorted={sorted} />
				</Button>
			) : (
				content
			)}
			{canResize && (
				<button
					aria-label="Resize column"
					className={clsx(
						"absolute top-0 right-0 h-full w-1 cursor-col-resize select-none touch-none border-none p-0",
						header.column.getIsResizing()
							? "bg-interaction-pressed"
							: "bg-transparent hover:bg-interaction-hover",
					)}
					onDoubleClick={() => header.column.resetSize()}
					onMouseDown={header.getResizeHandler()}
					onTouchStart={header.getResizeHandler()}
					type="button"
				/>
			)}
		</TableHeaderCell>
	);
}

function DataRows({ table }: { table: Table<RowData> }) {
	return (
		<TableBody>
			{table.getRowModel().rows.map((row) => (
				<TableRow key={row.id}>
					{row.getVisibleCells().map((cell) => (
						<TableDataCell
							key={cell.id}
							style={{
								width: `calc(var(--col-${cell.column.id}-size) * 1px)`,
							}}
						>
							{flexRender(cell.column.columnDef.cell, cell.getContext())}
						</TableDataCell>
					))}
				</TableRow>
			))}
		</TableBody>
	);
}

const MemoizedDataRows = memo(DataRows, (prev, next) => {
	return prev.table.options.data === next.table.options.data;
}) as typeof DataRows;

interface ContentProps<TData extends RowData> {
	renderContent?: (props: { rows: Row<TData>[] }) => ReactNode;
	emptyMessage?: ReactNode;
	noResultsMessage?: ReactNode;
}

export function Content<TData extends RowData>({
	renderContent,
	emptyMessage = (
		<StatusMessage
			size="medium"
			slotHeading="No data"
			slotMedia={<CircleSlash size={32} />}
		/>
	),
	noResultsMessage = (
		<StatusMessage
			size="medium"
			slotHeading="No results found"
			slotMedia={<SearchX size={32} />}
			slotSubheading="Try adjusting your search or filter criteria."
		/>
	),
}: ContentProps<TData>) {
	const table = useDataTable("Content");

	// biome-ignore lint/correctness/useExhaustiveDependencies: columnSizing/columnSizingInfo trigger recomputation per TanStack docs
	const columnSizeVars = useMemo(() => {
		const vars: CSSProperties = {};
		for (const header of table.getFlatHeaders()) {
			vars[`--header-${header.id}-size`] = header.getSize();
			vars[`--col-${header.column.id}-size`] = header.column.getSize();
		}
		return vars;
	}, [table.getState().columnSizingInfo, table.getState().columnSizing]);

	const hasData = table.getCoreRowModel().rows.length > 0;
	const hasFilteredResults = table.getFilteredRowModel().rows.length > 0;
	const rows = table.getRowModel().rows;

	if (!hasData) {
		return <div className="py-8">{emptyMessage}</div>;
	}

	if (!hasFilteredResults) {
		return <div className="py-8">{noResultsMessage}</div>;
	}

	if (renderContent) {
		return <>{renderContent({ rows: rows as Row<TData>[] })}</>;
	}

	return (
		<div className="overflow-x-auto">
			<TableRoot
				className="w-full bg-transparent"
				layout="auto"
				style={columnSizeVars}
			>
				<TableHead>
					{table.getHeaderGroups().map((headerGroup) => (
						<TableRow key={headerGroup.id}>
							{headerGroup.headers.map((header) => (
								<HeaderCell header={header} key={header.id} />
							))}
						</TableRow>
					))}
				</TableHead>
				{table.getState().columnSizingInfo.isResizingColumn ? (
					<MemoizedDataRows table={table} />
				) : (
					<DataRows table={table} />
				)}
			</TableRoot>
		</div>
	);
}
