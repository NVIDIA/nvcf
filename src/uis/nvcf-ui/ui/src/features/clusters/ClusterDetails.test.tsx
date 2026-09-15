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

import { screen } from "@testing-library/react";
import { beforeAll, describe, expect, it } from "vitest";
import { getGetClusterForNcaIdAndClusterIdMockHandler } from "~/generated/api/clusters/clusters.msw";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { server } from "~/mocks/server";
import { renderWithRouter } from "~/testing/render";
import { clusterDetailRoute } from "./routes";

// TanStack Router's .lazy() uses a dynamic import() at runtime. Pre-warming it
// here via beforeAll ensures the module is in the dynamic-import cache before
// any test fires, avoiding a cold Vite transform under CI CPU pressure.
beforeAll(async () => {
	await import("./ClusterDetails");
});

const CLUSTER_ID = "c1";
const baseClstr = {
	clusterId: CLUSTER_ID,
	clusterName: "my-test-cluster",
};

describe("ClusterDetails", () => {
	it("renders cluster name and breadcrumb", async () => {
		server.use(
			getGetClusterForNcaIdAndClusterIdMockHandler({
				...baseClstr,
				status: GetClusterResponseStatus.READY,
			}),
		);

		renderWithRouter({
			routes: [clusterDetailRoute],
			initialLocation: `/clusters/${CLUSTER_ID}`,
		});

		const matches = await screen.findAllByText("my-test-cluster");
		expect(matches.length).toBeGreaterThan(0);
		const computeMatches = await screen.findAllByText("Compute Clusters");
		expect(computeMatches.length).toBeGreaterThan(0);
	});

	it("renders overview panel fields", async () => {
		server.use(
			getGetClusterForNcaIdAndClusterIdMockHandler({
				...baseClstr,
				status: GetClusterResponseStatus.READY,
				region: "us-east-1",
				nvcaVersion: "2.0.0",
				clusterGroupName: "test-group",
				clusterDescription: "A test cluster",
			}),
		);

		renderWithRouter({
			routes: [clusterDetailRoute],
			initialLocation: `/clusters/${CLUSTER_ID}`,
		});

		const matches = await screen.findAllByText("my-test-cluster");
		expect(matches.length).toBeGreaterThan(0);
		expect(screen.getByText("us-east-1")).toBeInTheDocument();
		expect(screen.getByText("2.0.0")).toBeInTheDocument();
		expect(screen.getByText("test-group")).toBeInTheDocument();
		expect(screen.getByText("A test cluster")).toBeInTheDocument();
	});

	it("renders GPU utilization bars", async () => {
		server.use(
			getGetClusterForNcaIdAndClusterIdMockHandler({
				...baseClstr,
				gpuUsage: {
					A100: { capacity: 10, available: 5 },
				},
			}),
		);

		renderWithRouter({
			routes: [clusterDetailRoute],
			initialLocation: `/clusters/${CLUSTER_ID}`,
		});

		const matches = await screen.findAllByText("my-test-cluster");
		expect(matches.length).toBeGreaterThan(0);
		expect(screen.getByText("GPU Utilization")).toBeInTheDocument();
		expect(screen.getByText("A100")).toBeInTheDocument();
		expect(screen.getByText("5/10 GPUs")).toBeInTheDocument();
	});

	it("renders empty state in instance configuration panel when gpus are absent", async () => {
		server.use(
			getGetClusterForNcaIdAndClusterIdMockHandler({
				...baseClstr,
				status: GetClusterResponseStatus.NOT_READY,
			}),
		);

		renderWithRouter({
			routes: [clusterDetailRoute],
			initialLocation: `/clusters/${CLUSTER_ID}`,
		});

		expect(
			await screen.findByText("Instance Configuration"),
		).toBeInTheDocument();
		expect(
			screen.getByText("No Instance Configuration Yet"),
		).toBeInTheDocument();
	});

	it("renders instance configuration tabs when gpus are present", async () => {
		server.use(
			getGetClusterForNcaIdAndClusterIdMockHandler({
				...baseClstr,
				gpus: [
					{
						name: "A100",
						capacity: 8,
						instanceTypes: [
							{
								name: "OCI.GPU.A100_1x",
								value: "OCI.GPU.A100",
								cpuCores: 32,
								systemMemory: "251Gi",
								gpuCount: 1,
								gpuMemory: "80Gi",
								description: "NVIDIA A100 on OCI",
							},
						],
					},
				],
			}),
		);

		renderWithRouter({
			routes: [clusterDetailRoute],
			initialLocation: `/clusters/${CLUSTER_ID}`,
		});

		const matches = await screen.findAllByText("my-test-cluster");
		expect(matches.length).toBeGreaterThan(0);
		const gpuMatches = await screen.findAllByText("A100");
		expect(gpuMatches.length).toBeGreaterThan(0);
		expect(screen.getByText("Instance Configuration")).toBeInTheDocument();
	});
});
