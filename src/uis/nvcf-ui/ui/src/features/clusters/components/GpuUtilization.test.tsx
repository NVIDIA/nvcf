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
import { GpuUtilization } from "./GpuUtilization";

describe("GpuUtilization", () => {
	it("derives used/capacity and percentage from availability", () => {
		render(
			<GpuUtilization name="H100" usage={{ capacity: 10, available: 4 }} />,
		);
		// used = capacity - available = 6; pct = 60%.
		expect(screen.getByText("6/10 GPUs")).toBeInTheDocument();
		expect(screen.getByText("60%")).toBeInTheDocument();
	});

	it("renders 0% when capacity is zero (no divide-by-zero)", () => {
		render(
			<GpuUtilization name="H100" usage={{ capacity: 0, available: 0 }} />,
		);
		expect(screen.getByText("0/0 GPUs")).toBeInTheDocument();
		expect(screen.getByText("0%")).toBeInTheDocument();
	});
});
