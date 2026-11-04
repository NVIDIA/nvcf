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
import { InstanceConfigPanel } from "~/features/clusters/components/InstanceConfigPanel";

describe("InstanceConfigPanel", () => {
	it("shows an empty state when the cluster reports no GPUs", () => {
		render(<InstanceConfigPanel gpus={[]} usage={{}} />);
		expect(
			screen.getByText("No Instance Configuration Yet"),
		).toBeInTheDocument();
	});

	it("shows GPU capacity and the selected instance type's details", () => {
		render(
			<InstanceConfigPanel
				gpus={[
					{
						name: "A100",
						instanceTypes: [
							{
								name: "OCI.GPU.A100_1x",
								value: "OCI.GPU.A100",
								default: true,
								cpuCores: 32,
								systemMemory: "251Gi",
								gpuCount: 1,
								gpuMemory: "80Gi",
								description: "NVIDIA A100 on OCI",
							},
						],
					},
				]}
				usage={{ A100: { capacity: 8, available: 3 } }}
			/>,
		);

		expect(screen.getByRole("tab", { name: "A100" })).toBeInTheDocument();
		expect(
			screen.getByRole("tab", { name: "OCI.GPU.A100_1x" }),
		).toBeInTheDocument();
		expect(screen.getByText("8")).toBeInTheDocument();
		expect(screen.getByText("Yes")).toBeInTheDocument();
		expect(screen.getByText("251Gi")).toBeInTheDocument();
		expect(screen.getByText("NVIDIA A100 on OCI")).toBeInTheDocument();
	});
});
