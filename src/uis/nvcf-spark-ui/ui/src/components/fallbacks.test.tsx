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

import { createRoute } from "@tanstack/react-router";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { registryRoute } from "~/features/registry/routes";
import { HttpError } from "~/lib/fetch";
import { handlers as grafanaDown } from "~/mocks/scenarios/registry/grafana-down";
import { server } from "~/mocks/server";
import { rootRoute } from "~/rootRoute";
import { renderWithRouter } from "~/testing/render";

describe("NotFound", () => {
	it("sends an unknown route back to the registry", async () => {
		renderWithRouter({ routes: [registryRoute], initialLocation: "/nowhere" });

		expect(
			await screen.findByRole("heading", { name: "Page not found" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Go to endpoint registry" }),
		).toHaveAttribute("href", "/registry");
	});

	it("says Grafana is unavailable when its link lands on the UI", async () => {
		server.use(...grafanaDown);
		renderWithRouter({
			routes: [registryRoute],
			initialLocation: "/grafana/d/llm-demo?var-model=GLM-5.3-UD-IQ2_M",
		});

		expect(
			await screen.findByRole("heading", { name: "Grafana unavailable" }),
		).toBeInTheDocument();
		expect(screen.getByText("/grafana/d/llm-demo")).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Go to endpoint registry" }),
		).toHaveAttribute("href", "/registry");
		expect(screen.queryByText("Page not found")).not.toBeInTheDocument();
	});

	it("keeps Page not found when Grafana lives elsewhere", async () => {
		// The baseline config points at another origin's Grafana.
		renderWithRouter({
			routes: [registryRoute],
			initialLocation: "/grafana/d/llm-demo",
		});

		expect(
			await screen.findByRole("heading", { name: "Page not found" }),
		).toBeInTheDocument();
	});

	it("keeps Page not found when the config can't be read", async () => {
		server.use(
			http.get("/api/v1/config", () =>
				HttpResponse.json({ message: "Not Found" }, { status: 404 }),
			),
		);
		renderWithRouter({
			routes: [registryRoute],
			initialLocation: "/grafana/d/llm-demo",
		});

		expect(
			await screen.findByRole("heading", { name: "Page not found" }),
		).toBeInTheDocument();
	});
});

describe("RouteErrorFallback", () => {
	it("shows the error with the API's message, and retries the loader", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		const failing = createRoute({
			getParentRoute: () => rootRoute,
			path: "failing",
			loader: () => {
				attempts += 1;
				throw new HttpError(
					new Response(null, {
						status: 500,
						statusText: "Internal Server Error",
					}),
					{
						message: "Something broke upstream",
					},
				);
			},
			component: () => null,
		});
		renderWithRouter({ routes: [failing], initialLocation: "/failing" });

		expect(
			await screen.findByRole("heading", { name: "500 Internal Server Error" }),
		).toBeInTheDocument();
		expect(screen.getByText("Something broke upstream")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Try again" }));

		expect(attempts).toBeGreaterThan(1);
	});
});
