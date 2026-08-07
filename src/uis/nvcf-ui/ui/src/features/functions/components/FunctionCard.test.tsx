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
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { FunctionWithDeployment } from "../types";
import { FunctionCard } from "./FunctionCard";

vi.mock("@tanstack/react-router", () => ({
	Link: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("~/components/OverflowGroup", () => ({
	OverflowGroup: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

const base: FunctionWithDeployment = {
	id: "fn-1",
	ncaId: "nca-1",
	versionId: "v-1",
	name: "my-function",
	status: "ACTIVE",
	createdAt: "2026-01-01T00:00:00Z",
	lastUpdatedAt: "2026-01-01T00:00:00Z",
	functionType: "DEFAULT",
	healthUri: "https://example.com/health",
	deploymentSpecifications: [],
};

describe("FunctionCard", () => {
	it("renders the function name", () => {
		render(<FunctionCard fn={base} />);
		expect(screen.getByText("my-function")).toBeInTheDocument();
	});

	it("shows Container Function when helmChart is absent", () => {
		render(<FunctionCard fn={base} />);
		expect(screen.getByText("Container Function")).toBeInTheDocument();
	});

	it("shows Helm Function when helmChart is present", () => {
		render(<FunctionCard fn={{ ...base, helmChart: "my-chart:1.0.0" }} />);
		expect(screen.getByText("Helm Function")).toBeInTheDocument();
	});

	it("renders instance types when present", () => {
		const deploymentSpecifications = [
			{
				gpu: "A100",
				maxInstances: 1,
				minInstances: 0,
				instanceType: "A100.80GB.1x",
			},
			{
				gpu: "H100",
				maxInstances: 1,
				minInstances: 0,
				instanceType: "H100.80GB.2x",
			},
		];
		render(<FunctionCard fn={{ ...base, deploymentSpecifications }} />);
		expect(screen.getByText("A100.80GB.1x")).toBeInTheDocument();
		expect(screen.getByText("H100.80GB.2x")).toBeInTheDocument();
	});

	it("renders tags when present", () => {
		render(<FunctionCard fn={{ ...base, tags: ["gpu", "inference"] }} />);
		expect(screen.getByText("gpu")).toBeInTheDocument();
		expect(screen.getByText("inference")).toBeInTheDocument();
	});

	it("renders an em-dash for instance types and tags when absent", () => {
		render(<FunctionCard fn={base} />);
		expect(screen.getAllByText("—")).toHaveLength(2);
	});
});
