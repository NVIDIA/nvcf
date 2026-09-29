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

import type { Column, RowData } from "@tanstack/react-table";
import { describe, expect, it } from "vitest";
import { getColumnLabel } from "./columns";

type MockColumn = Column<RowData, unknown>;

const col = (id: string, header: unknown) =>
	({ id, columnDef: { header } }) as MockColumn;

describe("getColumnLabel", () => {
	it("returns the header string when header is a string", () => {
		expect(getColumnLabel(col("status", "Status"))).toBe("Status");
	});

	it("falls back to column id when header is a function", () => {
		expect(getColumnLabel(col("status", () => null))).toBe("status");
	});

	it("falls back to column id when header is undefined", () => {
		expect(getColumnLabel(col("status", undefined))).toBe("status");
	});
});
