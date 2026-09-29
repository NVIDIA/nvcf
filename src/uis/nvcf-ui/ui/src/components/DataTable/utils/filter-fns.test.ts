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

import type { Row, RowData } from "@tanstack/react-table";
import { describe, expect, it } from "vitest";
import { filterFns } from "./filter-fns";

const row = (value: unknown) =>
	({ getValue: () => value }) as unknown as Row<RowData>;

describe("filterFns.includesValue", () => {
	it("matches a string cell value case-insensitively", () => {
		expect(
			filterFns.includesValue(row("Hello World"), "name", "hello", () => {}),
		).toBe(true);
	});

	it("matches against individual items in an array cell value", () => {
		expect(
			filterFns.includesValue(
				row(["gpu", "inference"]),
				"tags",
				"infer",
				() => {},
			),
		).toBe(true);
	});

	it("returns false when no array item matches", () => {
		expect(
			filterFns.includesValue(
				row(["gpu", "inference"]),
				"tags",
				"vision",
				() => {},
			),
		).toBe(false);
	});

	it("returns false for a null cell value", () => {
		expect(filterFns.includesValue(row(null), "name", "foo", () => {})).toBe(
			false,
		);
	});

	it("autoRemove returns true for empty or nullish filter values", () => {
		expect(filterFns.includesValue.autoRemove?.("")).toBe(true);
		expect(filterFns.includesValue.autoRemove?.(null)).toBe(true);
		expect(filterFns.includesValue.autoRemove?.(undefined)).toBe(true);
	});
});

describe("filterFns.multiSelect", () => {
	it("returns true when the row value is in the filter array", () => {
		expect(
			filterFns.multiSelect(
				row("active"),
				"status",
				["active", "inactive"],
				() => {},
			),
		).toBe(true);
	});

	it("returns false when the row value is not in the filter array", () => {
		expect(
			filterFns.multiSelect(
				row("deleted"),
				"status",
				["active", "inactive"],
				() => {},
			),
		).toBe(false);
	});

	it("returns false for an empty filter array", () => {
		expect(filterFns.multiSelect(row("active"), "status", [], () => {})).toBe(
			false,
		);
	});

	it("returns false when filterValue is not an array", () => {
		expect(
			filterFns.multiSelect(row("active"), "status", "active", () => {}),
		).toBe(false);
	});
});
