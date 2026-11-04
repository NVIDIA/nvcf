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

import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { getGetClustersMockHandler } from "~/generated/api/clusters/clusters.msw";
import { getGetControlPlaneStatusMockHandler } from "~/generated/api/control-plane/control-plane.msw";
import { getGetAllFunctionDeploymentsMockHandler } from "~/generated/api/function-deployment/function-deployment.msw";
import { getGetAllFunctionsMockHandler } from "~/generated/api/function-management/function-management.msw";
import { FunctionDtoStatus } from "~/generated/model/functionDtoStatus";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { server } from "~/mocks/server";
import { renderWithRouter } from "~/testing/render";
import { dashboardRoute } from "./routes";

const baseFn = {
	ncaId: "nca-1",
	versionId: "v-1",
	healthUri: "https://example.com/health",
	createdAt: "2026-01-01T00:00:00Z",
	functionType: "DEFAULT" as const,
	status: FunctionDtoStatus.ACTIVE,
};

function render() {
	return renderWithRouter({ routes: [dashboardRoute], initialLocation: "/" });
}

describe("Dashboard", () => {
	describe("Stats Panels", () => {
		describe("ClusterStatsPanel", () => {
			it("shows correct counts by cluster status", async () => {
				server.use(
					getGetClustersMockHandler([
						{ clusterName: "c1", status: GetClusterResponseStatus.READY },
						{ clusterName: "c2", status: GetClusterResponseStatus.READY },
						{
							clusterName: "c3",
							status: GetClusterResponseStatus.UNHEALTHY,
						},
					]),
				);
				render();
				const readyLink = await screen.findByRole("link", { name: "Ready" });
				expect(readyLink.previousElementSibling).toHaveTextContent("2");
				const errorLink = screen.getByRole("link", { name: "Error" });
				expect(errorLink.previousElementSibling).toHaveTextContent("1");
			});
		});

		describe("ControlPlaneStatsPanel", () => {
			it("shows unhealthy count as a button when there are unhealthy components", async () => {
				server.use(
					getGetControlPlaneStatusMockHandler([
						{
							componentName: "api",
							namespace: "core",
							status: "unhealthy",
							timestamp: "2026-01-01T00:00:00Z",
						},
						{
							componentName: "worker",
							namespace: "core",
							status: "unhealthy",
							timestamp: "2026-01-01T00:00:00Z",
						},
					]),
				);
				render();
				const unhealthyButton = await screen.findByRole("button", {
					name: "Unhealthy",
				});
				expect(unhealthyButton.previousElementSibling).toHaveTextContent("2");
			});

			it("opens a modal listing unhealthy components when the button is clicked", async () => {
				const user = userEvent.setup();
				server.use(
					getGetControlPlaneStatusMockHandler([
						{
							componentName: "sis-api",
							namespace: "sis",
							status: "unhealthy",
							timestamp: "2026-01-01T00:00:00Z",
						},
						{
							componentName: "ess-api",
							namespace: "ess",
							status: "unhealthy",
							timestamp: "2026-01-01T00:00:00Z",
						},
					]),
				);
				render();
				await user.click(
					await screen.findByRole("button", { name: "Unhealthy" }),
				);
				expect(screen.getByText("sis-api")).toBeInTheDocument();
				expect(screen.getByText("ess-api")).toBeInTheDocument();
			});

			it("shows unhealthy count as plain text when all components are healthy", async () => {
				server.use(
					getGetControlPlaneStatusMockHandler([
						{
							componentName: "api",
							namespace: "core",
							status: "healthy",
							timestamp: "2026-01-01T00:00:00Z",
						},
					]),
				);
				render();
				await screen.findByText("Healthy");
				expect(
					screen.queryByRole("button", { name: "Unhealthy" }),
				).not.toBeInTheDocument();
			});
		});

		describe("FunctionStatsPanel", () => {
			it("counts active functions", async () => {
				server.use(
					getGetAllFunctionsMockHandler({
						functions: [
							{
								...baseFn,
								id: "f1",
								name: "fn-1",
								status: FunctionDtoStatus.ACTIVE,
							},
							{
								...baseFn,
								id: "f2",
								name: "fn-2",
								status: FunctionDtoStatus.ACTIVE,
							},
							{
								...baseFn,
								id: "f3",
								name: "fn-3",
								status: FunctionDtoStatus.INACTIVE,
							},
						],
					}),
				);
				render();
				const link = await screen.findByRole("link", {
					name: "Active",
				});
				expect(link.previousElementSibling).toHaveTextContent("2");
			});
			it("counts total functions", async () => {
				server.use(
					getGetAllFunctionsMockHandler({
						functions: [
							{
								...baseFn,
								id: "f1",
								name: "fn-1",
								status: FunctionDtoStatus.ACTIVE,
							},
							{
								...baseFn,
								id: "f2",
								name: "fn-2",
								status: FunctionDtoStatus.ACTIVE,
							},
							{
								...baseFn,
								id: "f3",
								name: "fn-3",
								status: FunctionDtoStatus.INACTIVE,
							},
						],
					}),
				);
				render();
				const link = await screen.findByRole("link", {
					name: "Total",
				});
				expect(link.previousElementSibling).toHaveTextContent("3");
			});
		});
	});

	describe("RecentFunctionsList", () => {
		const baseDeployment = {
			deploymentId: "d-1",
			ncaId: "nca-1",
			functionStatus: FunctionDtoStatus.ACTIVE,
			createdAt: "2026-01-01T00:00:00Z",
			lastUpdatedAt: "2026-01-01T00:00:00Z",
			deploymentSpecifications: [],
		};

		it("shows empty state when there are no functions", async () => {
			server.use(
				getGetAllFunctionsMockHandler({ functions: [] }),
				getGetAllFunctionDeploymentsMockHandler({ deployments: [] }),
			);
			render();
			expect(
				await screen.findByText("No deployed functions yet"),
			).toBeInTheDocument();
		});

		it("shows only the 5 most recently updated functions", async () => {
			server.use(
				getGetAllFunctionsMockHandler({
					functions: [
						{ ...baseFn, id: "f1", name: "fn-oldest", versionId: "v-1" },
						{ ...baseFn, id: "f2", name: "fn-2", versionId: "v-2" },
						{ ...baseFn, id: "f3", name: "fn-3", versionId: "v-3" },
						{ ...baseFn, id: "f4", name: "fn-4", versionId: "v-4" },
						{ ...baseFn, id: "f5", name: "fn-5", versionId: "v-5" },
						{ ...baseFn, id: "f6", name: "fn-newest", versionId: "v-6" },
					],
				}),
				getGetAllFunctionDeploymentsMockHandler({
					deployments: [
						{
							...baseDeployment,
							functionId: "f1",
							functionVersionId: "v-1",
							functionName: "fn-oldest",
							lastUpdatedAt: "2026-01-01T00:00:00Z",
						},
						{
							...baseDeployment,
							functionId: "f2",
							functionVersionId: "v-2",
							functionName: "fn-2",
							lastUpdatedAt: "2026-01-02T00:00:00Z",
						},
						{
							...baseDeployment,
							functionId: "f3",
							functionVersionId: "v-3",
							functionName: "fn-3",
							lastUpdatedAt: "2026-01-03T00:00:00Z",
						},
						{
							...baseDeployment,
							functionId: "f4",
							functionVersionId: "v-4",
							functionName: "fn-4",
							lastUpdatedAt: "2026-01-04T00:00:00Z",
						},
						{
							...baseDeployment,
							functionId: "f5",
							functionVersionId: "v-5",
							functionName: "fn-5",
							lastUpdatedAt: "2026-01-05T00:00:00Z",
						},
						{
							...baseDeployment,
							functionId: "f6",
							functionVersionId: "v-6",
							functionName: "fn-newest",
							lastUpdatedAt: "2026-01-06T00:00:00Z",
						},
					],
				}),
			);
			render();
			expect(await screen.findByText("fn-newest")).toBeInTheDocument();
			expect(screen.queryByText("fn-oldest")).not.toBeInTheDocument();
		});

		it("renders function name as a link to the function detail page", async () => {
			server.use(
				getGetAllFunctionsMockHandler({
					functions: [
						{ ...baseFn, id: "f1", name: "my-function", versionId: "v-1" },
					],
				}),
				getGetAllFunctionDeploymentsMockHandler({
					deployments: [
						{
							...baseDeployment,
							functionId: "f1",
							functionVersionId: "v-1",
							functionName: "my-function",
							lastUpdatedAt: "2026-01-01T00:00:00Z",
						},
					],
				}),
			);
			render();
			const link = await screen.findByRole("link", { name: "my-function" });
			expect(link).toHaveAttribute("href", "/functions/f1/versions/v-1");
		});

		it("shows instance types from deployment specifications", async () => {
			server.use(
				getGetAllFunctionsMockHandler({
					functions: [
						{ ...baseFn, id: "f1", name: "fn-with-gpu", versionId: "v-1" },
					],
				}),
				getGetAllFunctionDeploymentsMockHandler({
					deployments: [
						{
							...baseDeployment,
							functionId: "f1",
							functionVersionId: "v-1",
							functionName: "fn-with-gpu",
							deploymentSpecifications: [
								{
									gpu: "A100",
									backend: "AWS",
									instanceType: "A100.80GB.1x",
									maxInstances: 1,
									minInstances: 1,
								},
								{
									gpu: "H100",
									backend: "AWS",
									instanceType: "H100.80GB.2x",
									maxInstances: 1,
									minInstances: 1,
								},
							],
						},
					],
				}),
			);
			render();
			expect(await screen.findByText("A100.80GB.1x")).toBeInTheDocument();
			expect(screen.getByText("H100.80GB.2x")).toBeInTheDocument();
		});

		it("deduplicates repeated instance types", async () => {
			server.use(
				getGetAllFunctionsMockHandler({
					functions: [
						{ ...baseFn, id: "f1", name: "fn-dup", versionId: "v-1" },
					],
				}),
				getGetAllFunctionDeploymentsMockHandler({
					deployments: [
						{
							...baseDeployment,
							functionId: "f1",
							functionVersionId: "v-1",
							functionName: "fn-dup",
							deploymentSpecifications: [
								{
									gpu: "A100",
									backend: "AWS",
									instanceType: "A100.80GB.1x",
									maxInstances: 1,
									minInstances: 1,
								},
								{
									gpu: "A100",
									backend: "GCP",
									instanceType: "A100.80GB.1x",
									maxInstances: 1,
									minInstances: 1,
								},
							],
						},
					],
				}),
			);
			render();
			await screen.findByText("fn-dup");
			expect(screen.getAllByText("A100.80GB.1x")).toHaveLength(1);
		});

		it("shows function tags", async () => {
			server.use(
				getGetAllFunctionsMockHandler({
					functions: [
						{
							...baseFn,
							id: "f1",
							name: "fn-tagged",
							versionId: "v-1",
							tags: ["llm", "inference"],
						},
					],
				}),
				getGetAllFunctionDeploymentsMockHandler({
					deployments: [
						{
							...baseDeployment,
							functionId: "f1",
							functionVersionId: "v-1",
							functionName: "fn-tagged",
							lastUpdatedAt: "2026-01-01T00:00:00Z",
						},
					],
				}),
			);
			render();
			expect(await screen.findByText("llm")).toBeInTheDocument();
			expect(screen.getByText("inference")).toBeInTheDocument();
		});
	});
	describe("Ready Clusters", () => {
		it("displays ready cluster status, region, and GPU utilization", async () => {
			server.use(
				getGetClustersMockHandler([
					{
						clusterId: "c1",
						clusterName: "cluster-1",
						status: GetClusterResponseStatus.READY,
						region: "us-west-1",
						gpuUsage: {
							A100: { capacity: 10, available: 4 },
							V100: { capacity: 5, available: 1 },
						},
					},
				]),
			);
			render();
			const readyCardsSection = await screen.findByRole("region", {
				name: "Ready Compute Clusters",
			});
			await within(readyCardsSection).findByText("cluster-1");
			expect(within(readyCardsSection).getByText("Ready")).toBeInTheDocument();
			expect(
				within(readyCardsSection).getByText("us-west-1"),
			).toBeInTheDocument();
			expect(
				within(readyCardsSection).getByText("6/10 GPUs"),
			).toBeInTheDocument();
			expect(
				within(readyCardsSection).getByRole("progressbar", { name: /a100/i }),
			).toHaveAttribute("aria-valuetext", "60%");
			expect(
				within(readyCardsSection).getByText("4/5 GPUs"),
			).toBeInTheDocument();
			expect(
				within(readyCardsSection).getByRole("progressbar", { name: /v100/i }),
			).toHaveAttribute("aria-valuetext", "80%");
		});

		it("shows empty state when no clusters are ready", async () => {
			server.use(
				getGetClustersMockHandler([
					{ clusterName: "c1", status: GetClusterResponseStatus.UNHEALTHY },
				]),
			);
			render();
			const readyCardsSection = await screen.findByRole("region", {
				name: "Ready Compute Clusters",
			});
			expect(
				await within(readyCardsSection).findByText("No ready clusters"),
			).toBeInTheDocument();
		});

		it("paginates GPU types within a cluster card", async () => {
			const user = userEvent.setup();
			server.use(
				getGetClustersMockHandler([
					{
						clusterId: "c1",
						clusterName: "cluster-1",
						status: GetClusterResponseStatus.READY,
						gpuUsage: {
							A100: { capacity: 10, available: 4 },
							V100: { capacity: 5, available: 1 },
							H100: { capacity: 8, available: 8 },
						},
					},
				]),
			);
			render();
			const readyCardsSection = await screen.findByRole("region", {
				name: "Ready Compute Clusters",
			});
			await within(readyCardsSection).findByText("cluster-1");
			expect(
				within(readyCardsSection).getByRole("progressbar", { name: /a100/i }),
			).toBeInTheDocument();
			expect(
				within(readyCardsSection).getByRole("progressbar", { name: /v100/i }),
			).toBeInTheDocument();
			expect(
				within(readyCardsSection).queryByRole("progressbar", { name: /h100/i }),
			).not.toBeInTheDocument();
			await user.click(
				within(readyCardsSection).getByRole("tab", { name: "2" }),
			);
			expect(
				within(readyCardsSection).getByRole("progressbar", { name: /h100/i }),
			).toBeInTheDocument();
			expect(
				within(readyCardsSection).queryByRole("progressbar", { name: /a100/i }),
			).not.toBeInTheDocument();
		});
	});
});
