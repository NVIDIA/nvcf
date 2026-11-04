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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { OverflowGroup } from "./OverflowGroup";

// In tests, offsetWidth is 0 for all elements but gap (8px default) is still
// applied, so only the first child fits. This makes overflow behavior
// deterministic: N items → visibleCount = 1, "+N-1" overflow button shown.

describe("OverflowGroup", () => {
	it("renders children in the visible container", () => {
		render(
			<OverflowGroup>
				<span>Only Item</span>
			</OverflowGroup>,
		);
		const visible = screen
			.getAllByText("Only Item")
			.find((el) => !el.closest("[aria-hidden]"));
		expect(visible).toBeInTheDocument();
		expect(screen.queryByText(/^\+/)).not.toBeInTheDocument();
	});

	it("shows an overflow button for items that do not fit", () => {
		render(
			<OverflowGroup>
				<span>Item 1</span>
				<span>Item 2</span>
				<span>Item 3</span>
			</OverflowGroup>,
		);
		expect(screen.getByText("+2")).toBeInTheDocument();
	});

	it("expands to show all items when overflow button is clicked", async () => {
		render(
			<OverflowGroup kind="expand">
				<span>Item 1</span>
				<span>Item 2</span>
				<span>Item 3</span>
			</OverflowGroup>,
		);
		await userEvent.click(screen.getByText("+2"));
		expect(screen.getByText("See Less")).toBeInTheDocument();
	});

	it("collapses back when See Less is clicked", async () => {
		render(
			<OverflowGroup kind="expand">
				<span>Item 1</span>
				<span>Item 2</span>
				<span>Item 3</span>
			</OverflowGroup>,
		);
		await userEvent.click(screen.getByText("+2"));
		await userEvent.click(screen.getByText("See Less"));
		expect(screen.queryByText("See Less")).not.toBeInTheDocument();
		expect(screen.getByText("+2")).toBeInTheDocument();
	});
});
