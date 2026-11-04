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
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { getGetCloudAccountsMockHandler } from "~/generated/api/account/account.msw";
import { getGetFunctionDeploymentQueryOptions } from "~/generated/api/function-deployment/function-deployment";
import {
	getGetFunctionDeploymentMockHandler,
	getGetFunctionDeploymentResponseMock,
} from "~/generated/api/function-deployment/function-deployment.msw";
import {
	getGetFunctionVersionMockHandler,
	getGetFunctionVersionResponseMock,
} from "~/generated/api/function-management/function-management.msw";
import { FunctionDtoStatus } from "~/generated/model/functionDtoStatus";
import { handlers as noDeploymentHandlers } from "~/mocks/scenarios/functions/no-deployment";
import { server } from "~/mocks/server";
import { renderWithRouter } from "~/testing/render";

const ncaId = "test-nca-id";
const functionId = "fn-1";
const versionId = "v-1";

const testAccount = {
	ncaId,
	name: "Test Account",
	maxFunctionsAllowed: 10,
	maxTasksAllowed: 10,
	maxTelemetriesAllowed: 10,
	maxRegistryCredentialsAllowed: 10,
};

const baseFn = {
	...getGetFunctionVersionResponseMock().function,
	id: functionId,
	versionId,
	name: "my-function",
	status: FunctionDtoStatus.ACTIVE,
};

function renderDetail(fn = baseFn) {
	return renderWithRouter({
		initialLocation: `/functions/${fn.id}/versions/${fn.versionId}`,
	});
}

describe("FunctionDetail", () => {
	it("renders the function name in the page heading", async () => {
		server.use(getGetFunctionVersionMockHandler({ function: baseFn }));

		renderDetail();

		// Name appears in both the breadcrumb and the page heading
		const matches = await screen.findAllByText("my-function");
		expect(matches.length).toBeGreaterThan(0);
	});

	it("shows Container Details panel for a container function", async () => {
		server.use(
			getGetFunctionVersionMockHandler({
				function: { ...baseFn, helmChart: undefined },
			}),
		);

		renderDetail();

		expect(await screen.findByText("Container Details")).toBeInTheDocument();
		expect(screen.queryByText("Helm Chart Details")).not.toBeInTheDocument();
	});

	it("shows Helm Chart Details panel for a helm function", async () => {
		server.use(
			getGetFunctionVersionMockHandler({
				function: { ...baseFn, helmChart: "oci://example.com/chart" },
			}),
		);

		renderDetail();

		expect(await screen.findByText("Helm Chart Details")).toBeInTheDocument();
		expect(screen.queryByText("Container Details")).not.toBeInTheDocument();
	});

	it("shows static 'No Instance Type Yet' for INACTIVE status without prefetching deployment", async () => {
		server.use(
			getGetCloudAccountsMockHandler({ cloudAccounts: [testAccount] }),
			getGetFunctionVersionMockHandler({
				function: { ...baseFn, status: FunctionDtoStatus.INACTIVE },
			}),
		);

		const { queryClient } = renderDetail();

		expect(await screen.findByText("No Instance Type Yet")).toBeInTheDocument();
		expect(
			queryClient.getQueryState(
				getGetFunctionDeploymentQueryOptions(ncaId, functionId, versionId)
					.queryKey,
			),
		).toBeUndefined();
	});

	it("shows static 'No Instance Type Yet' for DELETED status without prefetching deployment", async () => {
		server.use(
			getGetCloudAccountsMockHandler({ cloudAccounts: [testAccount] }),
			getGetFunctionVersionMockHandler({
				function: { ...baseFn, status: FunctionDtoStatus.DELETED },
			}),
		);

		const { queryClient } = renderDetail();

		expect(await screen.findByText("No Instance Type Yet")).toBeInTheDocument();
		expect(
			queryClient.getQueryState(
				getGetFunctionDeploymentQueryOptions(ncaId, functionId, versionId)
					.queryKey,
			),
		).toBeUndefined();
	});

	it("shows 'No Instance Type Yet' when the function has no deployment specifications", async () => {
		server.use(
			getGetFunctionVersionMockHandler({ function: baseFn }),
			...noDeploymentHandlers,
		);

		renderDetail();

		// Two sequential MSW delays (function + deployment) — extend timeout
		expect(
			await screen.findByText("No Instance Type Yet", {}, { timeout: 3000 }),
		).toBeInTheDocument();
	});

	it("shows instance types grouped by GPU for a deployed function", async () => {
		const deployment = getGetFunctionDeploymentResponseMock().deployment;
		const spec = deployment.deploymentSpecifications[0];
		server.use(
			getGetFunctionVersionMockHandler({ function: baseFn }),
			getGetFunctionDeploymentMockHandler({
				deployment: {
					...deployment,
					deploymentSpecifications: [
						{
							...spec,
							gpu: "A100",
							instanceType: "A100.80GB.1x",
							systemMemory: "251Gi",
							minInstances: 1,
							maxInstances: 2,
						},
					],
				},
			}),
		);

		renderDetail();

		expect(
			await screen.findByRole("tab", { name: "A100" }, { timeout: 3000 }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("tab", { name: "A100.80GB.1x" }),
		).toBeInTheDocument();
		expect(screen.getByText("251Gi")).toBeInTheDocument();
		expect(screen.getByText("1 / 2")).toBeInTheDocument();
	});

	it("keeps a deployment failure inside the instance types panel and recovers on retry", async () => {
		const user = userEvent.setup();
		server.use(
			getGetFunctionVersionMockHandler({ function: baseFn }),
			http.get(
				"/v2/nvcf/accounts/:ncaId/deployments/functions/:functionId/versions/:functionVersionId",
				() => new HttpResponse(null, { status: 500 }),
			),
		);

		renderDetail();

		expect(
			await screen.findByText(
				"Failed to load instance types.",
				{},
				{ timeout: 3000 },
			),
		).toBeInTheDocument();
		// The rest of the page still renders.
		expect(screen.getByText("Basic Details")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /try again/i }),
		).not.toBeInTheDocument();

		server.use(...noDeploymentHandlers);
		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(
			await screen.findByText("No Instance Type Yet", {}, { timeout: 3000 }),
		).toBeInTheDocument();
	});

	it("shows error state when the function API fails", async () => {
		server.use(
			http.get(
				"/v2/nvcf/accounts/:ncaId/functions/:functionId/versions/:functionVersionId",
				() => new HttpResponse(null, { status: 500 }),
			),
		);

		renderDetail();

		expect(
			await screen.findByRole("button", { name: /try again/i }),
		).toBeInTheDocument();
	});
});
