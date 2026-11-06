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
import { InstanceTypesPanel } from "~/features/functions/components/InstanceTypesPanel";
import {
	getGetFunctionDeploymentMockHandler,
	getGetFunctionDeploymentResponseMock,
} from "~/generated/api/function-deployment/function-deployment.msw";
import { handlers as noDeploymentHandlers } from "~/mocks/scenarios/functions/no-deployment";
import { server } from "~/mocks/server";
import { renderWithQueryClient } from "~/testing/render";

function renderPanel() {
	return renderWithQueryClient(
		<InstanceTypesPanel
			functionId="fn-1"
			ncaId="test-nca-id"
			versionId="v-1"
		/>,
	);
}

describe("InstanceTypesPanel", () => {
	it("shows an empty state when the deployment has no specifications", async () => {
		server.use(...noDeploymentHandlers);

		renderPanel();

		expect(
			await screen.findByText("No Instance Type Yet", {}, { timeout: 3000 }),
		).toBeInTheDocument();
	});

	it("shows instance types grouped by GPU", async () => {
		const deployment = getGetFunctionDeploymentResponseMock().deployment;
		const spec = deployment.deploymentSpecifications[0];
		server.use(
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

		renderPanel();

		expect(
			await screen.findByRole("tab", { name: "A100" }, { timeout: 3000 }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("tab", { name: "A100.80GB.1x" }),
		).toBeInTheDocument();
		expect(screen.getByText("251Gi")).toBeInTheDocument();
		expect(screen.getByText("1 / 2")).toBeInTheDocument();
	});

	it("shows an error with Retry when the deployment fails, and recovers", async () => {
		const user = userEvent.setup();
		server.use(
			http.get(
				"/v2/nvcf/accounts/:ncaId/deployments/functions/:functionId/versions/:functionVersionId",
				() => new HttpResponse(null, { status: 500 }),
			),
		);

		renderPanel();

		expect(
			await screen.findByText(
				"Failed to load instance types.",
				{},
				{ timeout: 3000 },
			),
		).toBeInTheDocument();

		server.use(...noDeploymentHandlers);
		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(
			await screen.findByText("No Instance Type Yet", {}, { timeout: 3000 }),
		).toBeInTheDocument();
	});
});
