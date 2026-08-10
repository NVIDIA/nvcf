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
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { getGetAllFunctionsMockHandler } from "~/generated/api/function-management/function-management.msw";
import { FunctionDtoStatus } from "~/generated/model/functionDtoStatus";
import { handlers as emptyListHandlers } from "~/mocks/scenarios/functions/empty-list";
import { server } from "~/mocks/server";
import { renderWithRouter } from "~/testing/render";
import { functionsListRoute } from "./routes";

const baseFn = {
	ncaId: "nca-1",
	versionId: "v-1",
	status: FunctionDtoStatus.ACTIVE,
	healthUri: "https://example.com/health",
	createdAt: "2026-01-01T00:00:00Z",
	functionType: "DEFAULT" as const,
};

describe("FunctionsList", () => {
	it("renders a list of functions", async () => {
		server.use(
			getGetAllFunctionsMockHandler({
				functions: [{ ...baseFn, id: "fn-1", name: "my-function" }],
			}),
		);

		renderWithRouter({
			routes: [functionsListRoute],
			initialLocation: "/functions",
		});

		expect(await screen.findByText("my-function")).toBeInTheDocument();
	});

	it("shows empty state when no functions are returned", async () => {
		server.use(...emptyListHandlers);

		renderWithRouter({
			routes: [functionsListRoute],
			initialLocation: "/functions",
		});

		expect(await screen.findByText("No data")).toBeInTheDocument();
	});

	it("shows error state when the API fails", async () => {
		server.use(
			http.get(
				"/v2/nvcf/accounts/:ncaId/functions",
				() => new HttpResponse(null, { status: 500 }),
			),
		);

		renderWithRouter({
			routes: [functionsListRoute],
			initialLocation: "/functions",
		});

		expect(
			await screen.findByRole("button", { name: /try again/i }),
		).toBeInTheDocument();
	});
});
