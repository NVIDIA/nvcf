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
import { describe, expect, it } from "vitest";
import { DetailList } from "./DetailList";

describe("DetailList", () => {
	it("renders a label and string value", () => {
		render(<DetailList items={[{ label: "Name", value: "my-function" }]} />);
		expect(screen.getByText("Name")).toBeInTheDocument();
		expect(screen.getByText("my-function")).toBeInTheDocument();
	});

	it("renders an em dash when value is undefined", () => {
		render(<DetailList items={[{ label: "Description" }]} />);
		expect(screen.getByText("—")).toBeInTheDocument();
	});

	it("renders an em dash when value is null", () => {
		render(<DetailList items={[{ label: "Description", value: null }]} />);
		expect(screen.getByText("—")).toBeInTheDocument();
	});

	it("renders a number value as text", () => {
		render(<DetailList items={[{ label: "Port", value: 8080 }]} />);
		expect(screen.getByText("8080")).toBeInTheDocument();
	});

	it("renders a ReactNode value directly", () => {
		render(
			<DetailList
				items={[
					{ label: "Status", value: <span data-testid="badge">Active</span> },
				]}
			/>,
		);
		expect(screen.getByTestId("badge")).toBeInTheDocument();
		expect(screen.getByText("Active")).toBeInTheDocument();
	});

	it("skips false entries in the items array", () => {
		render(
			<DetailList
				items={[
					{ label: "Name", value: "my-function" },
					false,
					{ label: "Port", value: 8080 },
				]}
			/>,
		);
		expect(screen.getByText("Name")).toBeInTheDocument();
		expect(screen.getByText("Port")).toBeInTheDocument();
	});

	it("renders a tooltip trigger when tooltip is provided", () => {
		render(
			<DetailList
				items={[
					{
						label: "Rate Limit",
						value: "100/min",
						tooltip: <span>Tooltip content</span>,
					},
				]}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "More information about Rate Limit" }),
		).toBeInTheDocument();
	});
});
