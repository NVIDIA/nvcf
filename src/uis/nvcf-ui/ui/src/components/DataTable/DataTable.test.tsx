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

import type { ColumnDef, PaginationState } from "@tanstack/react-table";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DataTable } from "./index";

interface TestRow {
	name: string;
	status: string;
	region: string;
	invocations: number;
	createdAt: string;
}

const defaultData: TestRow[] = [
	{
		name: "llm-gateway",
		status: "active",
		region: "us-east",
		invocations: 500,
		createdAt: "2026-01-15",
	},
	{
		name: "image-gen",
		status: "inactive",
		region: "us-west",
		invocations: 150,
		createdAt: "2026-03-01",
	},
	{
		name: "speech-to-text",
		status: "active",
		region: "us-east",
		invocations: 300,
		createdAt: "2026-02-10",
	},
];

const columns: ColumnDef<TestRow, unknown>[] = [
	{ accessorKey: "name", header: "Name" },
	{
		accessorKey: "status",
		header: "Status",
		enableSorting: false,
		enableGlobalFilter: false,
		filterFn: "multiSelect",
		meta: {
			filterVariant: "multi-select",
			filterOptions: [
				{ label: "Active", value: "active" },
				{ label: "Inactive", value: "inactive" },
			],
		},
	},
	{
		accessorKey: "region",
		header: "Region",
		enableSorting: false,
		enableGlobalFilter: false,
		meta: {
			filterVariant: "single-select",
			filterOptions: [
				{ label: "US East", value: "us-east" },
				{ label: "US West", value: "us-west" },
			],
		},
	},
	{
		accessorKey: "invocations",
		header: "Invocations",
		meta: { sortType: "number" },
	},
	{
		accessorKey: "createdAt",
		header: "Created",
		meta: { sortType: "date" },
	},
];

function columnCells(headerName: string) {
	const table = within(screen.getByRole("table"));
	const headers = table.getAllByRole("columnheader");
	const colIndex = headers.findIndex((h) =>
		h.textContent?.includes(headerName),
	);
	return table
		.getAllByRole("row")
		.slice(1)
		.map((row) => within(row).getAllByRole("cell")[colIndex].textContent);
}

function renderTable(data: TestRow[] = defaultData) {
	return render(
		<DataTable columns={columns} data={data}>
			<DataTable.Toolbar>
				<DataTable.Search />
				<DataTable.ItemCount label="item" />
				<DataTable.Sort />
				<DataTable.Filters />
				<DataTable.ColumnVisibility />
			</DataTable.Toolbar>
			<DataTable.ActiveFilters />
			<DataTable.Content />
			<DataTable.Pagination />
		</DataTable>,
	);
}

describe("DataTable", () => {
	describe("Content", () => {
		it("renders column headers", () => {
			renderTable();
			expect(
				screen.getByRole("columnheader", { name: /Name/ }),
			).toBeInTheDocument();
			expect(
				screen.getByRole("columnheader", { name: /Status/ }),
			).toBeInTheDocument();
		});

		it("renders all data rows", () => {
			renderTable();
			const table = screen.getByRole("table");
			expect(within(table).getAllByRole("row")).toHaveLength(4);
		});

		it("shows empty state when data is empty", () => {
			renderTable([]);
			expect(screen.getByText("No data")).toBeInTheDocument();
			expect(screen.queryByRole("table")).not.toBeInTheDocument();
		});

		it("shows no results state when all rows are filtered out", () => {
			render(
				<DataTable
					columns={columns}
					data={defaultData}
					reactTableOptions={{
						state: { globalFilter: "joel embiid" },
					}}
				>
					<DataTable.Content />
				</DataTable>,
			);
			expect(screen.getByText("No results found")).toBeInTheDocument();
			expect(screen.queryByRole("table")).not.toBeInTheDocument();
		});

		it("renders custom content via renderContent", () => {
			render(
				<DataTable columns={columns} data={defaultData}>
					<DataTable.Content
						renderContent={({ rows }) => (
							<ul>
								{rows.map((row) => (
									<li key={row.id}>{row.getValue("name")}</li>
								))}
							</ul>
						)}
					/>
				</DataTable>,
			);
			expect(screen.getByRole("list")).toBeInTheDocument();
			expect(screen.getAllByRole("listitem")).toHaveLength(3);
		});
	});

	describe("Search", () => {
		it("filters rows and restores them when cleared", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.type(screen.getByRole("textbox"), "llm");
			await waitFor(() => {
				expect(
					screen.getByRole("cell", { name: "llm-gateway" }),
				).toBeInTheDocument();
				expect(
					screen.queryByRole("cell", { name: "image-gen" }),
				).not.toBeInTheDocument();
				expect(
					screen.queryByRole("cell", { name: "speech-to-text" }),
				).not.toBeInTheDocument();
				expect(screen.getByText("1 item")).toBeInTheDocument();
			});

			await user.clear(screen.getByRole("textbox"));
			await waitFor(() => {
				expect(
					screen.getByRole("cell", { name: "llm-gateway" }),
				).toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "image-gen" }),
				).toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "speech-to-text" }),
				).toBeInTheDocument();
				expect(screen.getByText("3 items")).toBeInTheDocument();
			});
		});
	});

	describe("Sort", () => {
		it("sorts text columns in both directions", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.click(
				screen.getByRole("menuitemradio", { name: "Name (A-Z)" }),
			);
			expect(columnCells("Name")).toEqual([
				"image-gen",
				"llm-gateway",
				"speech-to-text",
			]);

			await user.click(
				screen.getByRole("menuitemradio", { name: "Name (Z-A)" }),
			);
			expect(columnCells("Name")).toEqual([
				"speech-to-text",
				"llm-gateway",
				"image-gen",
			]);
		});

		it("sorts number columns in both directions", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.click(
				screen.getByRole("menuitemradio", {
					name: "Invocations (Low to High)",
				}),
			);
			expect(columnCells("Invocations")).toEqual(["150", "300", "500"]);

			await user.click(
				screen.getByRole("menuitemradio", {
					name: "Invocations (High to Low)",
				}),
			);
			expect(columnCells("Invocations")).toEqual(["500", "300", "150"]);
		});

		it("sorts date columns in both directions", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.click(
				screen.getByRole("menuitemradio", { name: "Created (Oldest First)" }),
			);
			expect(columnCells("Created")).toEqual([
				"2026-01-15",
				"2026-02-10",
				"2026-03-01",
			]);

			await user.click(
				screen.getByRole("menuitemradio", { name: "Created (Newest First)" }),
			);
			expect(columnCells("Created")).toEqual([
				"2026-03-01",
				"2026-02-10",
				"2026-01-15",
			]);
		});
	});

	describe("Filters", () => {
		it("filters rows by multi-select checkbox", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.click(
				screen.getByRole("menuitemcheckbox", { name: "Active" }),
			);
			await waitFor(() => {
				expect(
					screen.getByRole("cell", { name: "llm-gateway" }),
				).toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "speech-to-text" }),
				).toBeInTheDocument();
				expect(
					screen.queryByRole("cell", { name: "image-gen" }),
				).not.toBeInTheDocument();
				expect(screen.getByText("2 items")).toBeInTheDocument();
			});

			await user.click(
				screen.getAllByRole("menuitemcheckbox", { name: "Inactive" })[1],
			);
			await waitFor(() => {
				expect(
					screen.getByRole("cell", { name: "llm-gateway" }),
				).toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "image-gen" }),
				).toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "speech-to-text" }),
				).toBeInTheDocument();
				expect(screen.getByText("3 items")).toBeInTheDocument();
			});
		});

		it("filters rows by single-select radio", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.click(screen.getByRole("menuitemradio", { name: "US East" }));
			await waitFor(() => {
				expect(
					screen.getByRole("cell", { name: "llm-gateway" }),
				).toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "speech-to-text" }),
				).toBeInTheDocument();
				expect(
					screen.queryByRole("cell", { name: "image-gen" }),
				).not.toBeInTheDocument();
				expect(screen.getByText("2 items")).toBeInTheDocument();
			});

			await user.click(
				screen.getAllByRole("menuitemradio", { name: "US West" })[1],
			);
			await waitFor(() => {
				expect(
					screen.queryByRole("cell", { name: "llm-gateway" }),
				).not.toBeInTheDocument();
				expect(
					screen.getByRole("cell", { name: "image-gen" }),
				).toBeInTheDocument();
				expect(
					screen.queryByRole("cell", { name: "speech-to-text" }),
				).not.toBeInTheDocument();
				expect(screen.getByText("1 item")).toBeInTheDocument();
			});
		});
	});

	describe("Pagination", () => {
		const manyRows: TestRow[] = Array.from({ length: 5 }, (_, i) => ({
			name: `item-${i}`,
			status: "active",
			region: "us-east",
			invocations: i,
			createdAt: "2026-01-01",
		}));

		it("clamps pageIndex to last valid page when out of range", async () => {
			const onPaginationChange = vi.fn();
			// 5 rows, pageSize=2 → 3 pages (indices 0–2). pageIndex=5 is out of range.
			render(
				<DataTable
					columns={columns}
					data={manyRows}
					reactTableOptions={{
						state: { pagination: { pageIndex: 5, pageSize: 2 } },
						onPaginationChange,
					}}
				>
					<DataTable.Pagination />
				</DataTable>,
			);
			await waitFor(() => expect(onPaginationChange).toHaveBeenCalled());
			const updater = onPaginationChange.mock.calls[0][0] as (
				prev: PaginationState,
			) => PaginationState;
			expect(updater({ pageIndex: 5, pageSize: 2 }).pageIndex).toBe(2);
		});
	});

	describe("ActiveFilters", () => {
		it("displays filter tags and clears them", async () => {
			const user = userEvent.setup();
			renderTable();

			await user.click(
				screen.getByRole("menuitemcheckbox", { name: "Active" }),
			);
			await user.click(
				screen.getAllByRole("menuitemcheckbox", { name: "Inactive" })[0],
			);
			await waitFor(() => {
				expect(
					screen.getByText(
						(_text, el) => el?.textContent === "Status: Active, Inactive",
					),
				).toBeInTheDocument();
			});

			await user.click(screen.getByRole("button", { name: "Clear Filters" }));
			await waitFor(() => {
				expect(
					screen.queryByText(
						(_text, el) => el?.textContent === "Status: Active, Inactive",
					),
				).not.toBeInTheDocument();
			});
		});
	});
});
