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

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { getGetConfigMockHandler } from "~/generated/api/config/config.msw";
import { handlers as loading } from "~/mocks/scenarios/registry/loading";
import { handlers as stackDown } from "~/mocks/scenarios/registry/stack-down";
import { server } from "~/mocks/server";
import { GatewayStatus } from "./GatewayStatus";

function renderStatus() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>
			<GatewayStatus />
		</QueryClientProvider>,
	);
}

describe("GatewayStatus", () => {
	it("says the gateway is connected, with its public address", async () => {
		renderStatus();

		expect(await screen.findByText("Gateway connected")).toBeInTheDocument();
		// The short label is what phones show instead.
		expect(screen.getByText("Connected")).toBeInTheDocument();
		expect(
			await screen.findByText("llm-gateway.example.com"),
		).toBeInTheDocument();
	});

	it("leaves the address out when none is configured", async () => {
		server.use(getGetConfigMockHandler({}));
		renderStatus();

		expect(await screen.findByText("Gateway connected")).toBeInTheDocument();
		expect(
			screen.queryByText("llm-gateway.example.com"),
		).not.toBeInTheDocument();
	});

	it("says connecting before the first answer", () => {
		server.use(...loading);
		renderStatus();

		expect(screen.getAllByText("Connecting…")).toHaveLength(2);
	});

	it("says unreachable when the BFF can't reach the gateway", async () => {
		server.use(...stackDown);
		renderStatus();

		expect(await screen.findByText("Gateway unreachable")).toBeInTheDocument();
	});

	it("says unreachable on a network error", async () => {
		server.use(http.get("/v1/registry", () => HttpResponse.error()));
		renderStatus();

		expect(await screen.findByText("Gateway unreachable")).toBeInTheDocument();
	});

	it("says error when the gateway answers with a failure", async () => {
		server.use(
			http.get("/v1/registry", () =>
				HttpResponse.json({ message: "boom" }, { status: 500 }),
			),
		);
		renderStatus();

		expect(await screen.findByText("Gateway error")).toBeInTheDocument();
	});
});
