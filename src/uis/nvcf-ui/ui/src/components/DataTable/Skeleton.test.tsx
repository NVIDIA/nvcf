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

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DataTable } from "./index";

describe("DataTable.SkeletonContent", () => {
	it("renders default 7 rows and 4 columns", () => {
		render(<DataTable.SkeletonContent />);
		const table = screen.getByRole("table");
		const rows = within(table).getAllByRole("row");
		expect(rows).toHaveLength(8);
		expect(screen.getAllByRole("columnheader")).toHaveLength(4);
		expect(screen.getAllByRole("cell")).toHaveLength(28);
	});

	it("renders custom row and column counts", () => {
		render(<DataTable.SkeletonContent columns={6} rows={3} />);
		const table = screen.getByRole("table");
		const rows = within(table).getAllByRole("row");
		expect(rows).toHaveLength(4);
		expect(screen.getAllByRole("columnheader")).toHaveLength(6);
		expect(screen.getAllByRole("cell")).toHaveLength(18);
	});
});

describe("DataTable.Skeleton", () => {
	it("passes rows and columns to content", () => {
		render(<DataTable.Skeleton columns={3} rows={2} />);
		const table = screen.getByRole("table");
		const rows = within(table).getAllByRole("row");
		expect(rows).toHaveLength(3);
		expect(within(rows[0]).getAllByRole("columnheader")).toHaveLength(3);
	});
});
