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

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { playgroundRoute } from "~/features/playground/routes";
import { getGetConfigMockHandler } from "~/generated/api/config/config.msw";
import { handlers as emptyRegistry } from "~/mocks/scenarios/registry/empty";
import { handlers as stackDown } from "~/mocks/scenarios/registry/stack-down";
import { server } from "~/mocks/server";
import { getStoreHandlers, mockStore } from "~/mocks/store";
import { renderWithRouter } from "~/testing/render";
import { indexRoute, registryRoute } from "./routes";

function renderRegistry(location = "/registry") {
	return renderWithRouter({
		routes: [indexRoute, registryRoute, playgroundRoute],
		initialLocation: location,
	});
}

/** The detail panel's element, found by its heading: the selected model's name. */
async function detailPanelElement(model: string) {
	const heading = await screen.findByRole("heading", { level: 2, name: model });
	const panel = heading.closest("section");
	if (!panel) throw new Error("no detail panel");
	return panel;
}

async function detailPanel(model: string) {
	return within(await detailPanelElement(model));
}

describe("EndpointsList", () => {
	it("lists every registered model with its health", async () => {
		renderRegistry();

		const table = await screen.findByRole("table");
		const rows = within(table).getAllByRole("row").slice(1);
		expect(rows).toHaveLength(mockStore.registry.length);
		const unhealthy = within(table)
			.getByRole("link", { name: "deepseek-ai/deepseek-v4-flash" })
			.closest("tr");
		expect(unhealthy).toHaveTextContent("Unhealthy");
	});

	it("redirects / to the registry", async () => {
		const { router } = renderRegistry("/");

		expect(await screen.findByRole("table")).toBeInTheDocument();
		expect(router.state.location.pathname).toBe("/registry");
	});

	it("shows the first endpoint until one is selected", async () => {
		renderRegistry();

		await detailPanel("GLM-5.3-UD-IQ2_M");
		expect(
			screen.getByRole("link", { name: "GLM-5.3-UD-IQ2_M" }),
		).toHaveAttribute("aria-current", "true");
	});

	it("selects an endpoint by its link and keeps the selection in the URL", async () => {
		const user = userEvent.setup();
		const { router } = renderRegistry();

		await user.click(
			await screen.findByRole("link", { name: "meta/llama-3.1-8b-instruct" }),
		);

		await detailPanel("meta/llama-3.1-8b-instruct");
		expect(router.state.location.search).toEqual({
			model: "meta/llama-3.1-8b-instruct",
		});
	});

	it("selects an endpoint from anywhere on its row", async () => {
		const user = userEvent.setup();
		renderRegistry();

		const link = await screen.findByRole("link", { name: "qwen/qwen3.8-4b" });
		const row = link.closest("tr");
		if (!row) throw new Error("no row");
		await user.click(within(row).getByText("Healthy"));

		await detailPanel("qwen/qwen3.8-4b");
	});

	it("opens a deep-linked endpoint", async () => {
		renderRegistry("/registry?model=nvidia%2Fnemotron-5-super-49b");

		const panel = await detailPanel("nvidia/nemotron-5-super-49b");
		expect(panel.getByText("Unhealthy")).toBeInTheDocument();
		expect(
			panel.getByText(/No healthy server is registered for this model/),
		).toBeInTheDocument();
		expect(
			panel.getByRole("button", { name: "Try in playground" }),
		).toBeDisabled();
	});

	it("shows how to call a healthy endpoint", async () => {
		renderRegistry();

		const element = await detailPanelElement("GLM-5.3-UD-IQ2_M");
		const panel = within(element);
		expect(
			panel.getByText("https://llm-gateway.example.com/v1"),
		).toBeInTheDocument();
		expect(panel.getByText("/chat/completions")).toBeInTheDocument();
		// Syntax highlighting splits the snippet across elements, so match the
		// panel's text rather than a single element.
		expect(element).toHaveTextContent(
			'curl "https://llm-gateway.example.com/v1/chat/completions"',
		);
		expect(element).toHaveTextContent('"model": "GLM-5.3-UD-IQ2_M"');
		expect(
			panel.getByRole("link", { name: "Metrics in Grafana" }),
		).toHaveAttribute(
			"href",
			"https://grafana.example.com/d/llm-demo?var-model=GLM-5.3-UD-IQ2_M",
		);
		const playground = panel.getByRole("link", { name: "Try in playground" });
		expect(playground).toHaveAttribute(
			"href",
			"/playground?model=GLM-5.3-UD-IQ2_M",
		);
		expect(playground).toHaveAttribute("target", "_blank");
	});

	it("falls back to placeholders when the deployment sets no addresses", async () => {
		server.use(getGetConfigMockHandler({}));
		renderRegistry();

		const panel = await detailPanel("GLM-5.3-UD-IQ2_M");
		expect(panel.getByText("$GW_URL/v1")).toBeInTheDocument();
		expect(panel.getByText(/with the gateway's address/)).toBeInTheDocument();
		expect(
			panel.queryByRole("link", { name: "Metrics in Grafana" }),
		).not.toBeInTheDocument();
	});

	it("still loads when the config can't be read", async () => {
		server.use(
			http.get("/api/v1/config", () =>
				HttpResponse.json({ message: "Not Found" }, { status: 404 }),
			),
		);
		renderRegistry();

		const panel = await detailPanel("GLM-5.3-UD-IQ2_M");
		expect(panel.getByText("$GW_URL/v1")).toBeInTheDocument();
	});

	it("says when a deep-linked endpoint isn't registered", async () => {
		renderRegistry("/registry?model=gone%2Fmodel");

		expect(
			await screen.findByText("gone/model isn't registered"),
		).toBeInTheDocument();
	});

	it("shows the first-use state when nothing is registered", async () => {
		server.use(...emptyRegistry);
		renderRegistry();

		expect(
			await screen.findByText("Nothing is registered yet"),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Browse recipes" }),
		).toHaveAttribute("href", "/recipes");
	});

	it("shows the stack-down state, then reloads once the gateway is back", async () => {
		server.use(...stackDown);
		const { queryClient } = renderRegistry();

		expect(
			await screen.findByRole("heading", { name: "Gateway unreachable" }),
		).toBeInTheDocument();

		// The shell's poll finds the gateway back.
		server.use(...getStoreHandlers());
		await queryClient.refetchQueries({ queryKey: ["/v1/registry"] });

		expect(await screen.findByRole("table")).toBeInTheDocument();
	});

	it("retries a failed load on request", async () => {
		const user = userEvent.setup();
		server.use(...stackDown);
		renderRegistry();

		const retry = await screen.findByRole("button", { name: "Try again" });
		server.use(...getStoreHandlers());
		await user.click(retry);

		expect(await screen.findByRole("table")).toBeInTheDocument();
	});

	it("names a load error that isn't the stack being down", async () => {
		server.use(
			http.get("/v1/registry", () =>
				HttpResponse.json(
					{ error: { code: "x", message: "Registry exploded" } },
					{ status: 500 },
				),
			),
		);
		renderRegistry();

		expect(
			await screen.findByRole("heading", {
				name: "Couldn't load the registry",
			}),
		).toBeInTheDocument();
		expect(screen.getByText("Registry exploded")).toBeInTheDocument();
	});

	it("keeps the last registry when a refresh fails", async () => {
		const { queryClient } = renderRegistry();
		await screen.findByRole("table");

		server.use(...stackDown);
		await queryClient.refetchQueries({ queryKey: ["/v1/registry"] });

		expect(
			await screen.findByText(
				/Gateway unreachable\. Showing the registry as of/,
			),
		).toBeInTheDocument();
		expect(screen.getByRole("table")).toBeInTheDocument();
	});

	it("says a failed refresh isn't an outage when the gateway answered", async () => {
		const { queryClient } = renderRegistry();
		await screen.findByRole("table");

		server.use(
			http.get("/v1/registry", () =>
				HttpResponse.json({ message: "boom" }, { status: 500 }),
			),
		);
		await queryClient.refetchQueries({ queryKey: ["/v1/registry"] });

		expect(
			await screen.findByText(/Couldn't refresh the registry\./),
		).toBeInTheDocument();
	});
});

describe("untrusted model names", () => {
	// Anything that can register an endpoint picks its name, so a name is
	// untrusted input: it must render as text everywhere, including inside the
	// syntax-highlighted curl snippet.
	const hostile = `<img src=x onerror="alert(1)">`;

	it("renders a hostile model name as text, never as markup", async () => {
		server.use(
			http.get("/v1/registry", () =>
				HttpResponse.json({
					generatedAt: new Date().toISOString(),
					models: [
						{
							model: hostile,
							health: "Healthy",
							clusters: [
								{ clusterId: "c", registeredServers: 1, healthyServers: 1 },
							],
						},
					],
				}),
			),
		);
		renderRegistry();

		const element = await detailPanelElement(hostile);
		await waitFor(() =>
			expect(element).toHaveTextContent(
				`"model": "${hostile.replaceAll('"', '\\"')}"`,
			),
		);
		expect(document.querySelector("img")).toBeNull();
		expect(screen.getByRole("link", { name: hostile })).toBeInTheDocument();
	});
});

describe("health times", () => {
	it("shows since when an endpoint's health changed, and nothing before", async () => {
		const { queryClient } = renderRegistry();
		const panel = await detailPanel("GLM-5.3-UD-IQ2_M");
		expect(panel.queryByText(/^since /)).not.toBeInTheDocument();
		expect(panel.queryByText(/ago/)).not.toBeInTheDocument();

		server.use(
			http.get("/v1/registry", () =>
				HttpResponse.json({
					generatedAt: new Date().toISOString(),
					models: mockStore.registry.map((m) =>
						m.model === "GLM-5.3-UD-IQ2_M"
							? {
									...m,
									health: "Unhealthy",
									clusters: [{ ...m.clusters[0], healthyServers: 0 }],
								}
							: m,
					),
				}),
			),
		);
		await queryClient.refetchQueries({ queryKey: ["/v1/registry"] });

		expect(await panel.findByText("Unhealthy")).toBeInTheDocument();
		expect(panel.getByText(/^since \S/)).toBeInTheDocument();
	});
});
