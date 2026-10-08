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
import { getGetRecipesMockHandler } from "~/generated/api/recipes/recipes.msw";
import { handlers as emptyCatalog } from "~/mocks/scenarios/recipes/empty";
import { handlers as noCatalog } from "~/mocks/scenarios/recipes/unavailable";
import { handlers as stackDown } from "~/mocks/scenarios/registry/stack-down";
import { server } from "~/mocks/server";
import { mockStore } from "~/mocks/store";
import { renderWithRouter } from "~/testing/render";
import { recipeRoute, recipesRoute } from "./routes";

function renderRecipes(location = "/recipes") {
	return renderWithRouter({
		routes: [recipesRoute, recipeRoute],
		initialLocation: location,
	});
}

/** The card for a model, found by its heading. */
async function card(name: string) {
	const heading = await screen.findByRole("heading", { level: 2, name });
	const link = heading.closest("a");
	if (!link) throw new Error(`no card for ${name}`);
	return link;
}

function cardNames() {
	return screen
		.getAllByRole("heading", { level: 2 })
		.map((heading) => heading.textContent);
}

describe("RecipesList", () => {
	it("shows one card per model, deployable models first", async () => {
		renderRecipes();

		expect(
			await screen.findByRole("heading", {
				level: 1,
				name: "Model deployment recipes",
			}),
		).toBeInTheDocument();
		expect(await screen.findByText("7 recipes")).toBeInTheDocument();
		expect(cardNames()).toEqual([
			"GLM-5.3",
			"Nemotron 5 Super 49B",
			"Qwen3.8-27B",
			"Qwen3.8-Flash-Next",
			"DeepSeek V4 Flash",
			"Nemotron 5 Nano 12B",
			"Qwen3.8-4B",
		]);
	});

	it("summarizes a model's builds on its card, and opens its recipe", async () => {
		renderRecipes();

		const qwen = await card("Qwen3.8-27B");
		expect(qwen).toHaveAttribute("href", "/recipes/qwen3.8-27b");
		expect(qwen).toHaveTextContent("Instruction-tuned, 27B parameters");
		expect(qwen).toHaveTextContent("DGX Spark");
		expect(qwen).toHaveTextContent("FP8, NVFP4 · SGLang v0.5.19");
		expect(qwen).toHaveTextContent("Open recipe");

		const deepseek = await card("DeepSeek V4 Flash");
		expect(deepseek).toHaveTextContent("Not available");
		expect(deepseek).toHaveTextContent("No validated hardware yet");
		expect(deepseek).toHaveTextContent("Planned · checked 2026-10-07");

		expect(await card("Qwen3.8-4B")).toHaveTextContent(
			"No description in the catalog",
		);
	});

	it("marks models whose builds are registered with the gateway", async () => {
		renderRecipes();

		// The markers come from the registry, which may load after the catalog.
		expect(
			await within(await card("Qwen3.8-27B")).findByText("Deployed"),
		).toBeInTheDocument();
		expect(
			within(await card("Nemotron 5 Super 49B")).getByText(
				"Deployed, unhealthy",
			),
		).toBeInTheDocument();
		expect(await card("Qwen3.8-Flash-Next")).not.toHaveTextContent("Deployed");
	});

	it("still lists the recipes when the routing stack is down", async () => {
		server.use(...stackDown);
		renderRecipes();

		expect(await card("Qwen3.8-27B")).not.toHaveTextContent("Deployed");
		expect(screen.getByText("7 recipes")).toBeInTheDocument();
	});

	it("narrows by search at once, and keeps the search in the URL", async () => {
		const user = userEvent.setup();
		const { router } = renderRecipes();

		await user.type(
			await screen.findByRole("searchbox", { name: "Search recipes" }),
			"flash",
		);

		expect(screen.getByText("2 of 7 recipes")).toBeInTheDocument();
		expect(cardNames()).toEqual(["Qwen3.8-Flash-Next", "DeepSeek V4 Flash"]);
		await waitFor(() =>
			expect(router.state.location.search).toEqual({ q: "flash" }),
		);
	});

	it("opens a shared link with its search and hardware filter applied", async () => {
		renderRecipes("/recipes?q=nemotron&hardware=DGX%20Station");

		expect(await screen.findByText("1 of 7 recipes")).toBeInTheDocument();
		expect(cardNames()).toEqual(["Nemotron 5 Super 49B"]);
		expect(
			screen.getByRole("searchbox", { name: "Search recipes" }),
		).toHaveValue("nemotron");
	});

	it("ignores an unknown hardware filter in the URL", async () => {
		renderRecipes("/recipes?hardware=Laptop");

		expect(await screen.findByText("7 recipes")).toBeInTheDocument();
	});

	it("says when nothing matches, and resets the search", async () => {
		const user = userEvent.setup();
		const { router } = renderRecipes("/recipes?q=zzz");

		expect(
			await screen.findByRole("heading", { name: "No recipes match" }),
		).toBeInTheDocument();
		expect(screen.getByText("Nothing matches “zzz”.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Clear search" }));

		expect(await screen.findByText("7 recipes")).toBeInTheDocument();
		expect(
			screen.getByRole("searchbox", { name: "Search recipes" }),
		).toHaveValue("");
		expect(router.state.location.search).toEqual({});
	});

	it("says when no recipe fits the chosen hardware", async () => {
		const sparkOnly = mockStore.recipes.recipes.filter(
			(r) => r.id === "qwen3.8-27b",
		);
		server.use(
			getGetRecipesMockHandler({ schemaVersion: 1, recipes: sparkOnly }),
		);
		const user = userEvent.setup();
		renderRecipes("/recipes?hardware=DGX%20Station");

		expect(
			await screen.findByText("No recipe supports DGX Station yet."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Show all hardware" }));
		expect(await screen.findByText("1 recipe")).toBeInTheDocument();
	});

	it("names both resets when a search and a filter are applied", async () => {
		renderRecipes("/recipes?q=zzz&hardware=DGX%20Spark");

		expect(
			await screen.findByText("Nothing matches “zzz” on DGX Spark."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Clear search and hardware" }),
		).toBeInTheDocument();
	});

	it("explains an empty catalog", async () => {
		server.use(...emptyCatalog);
		renderRecipes();

		expect(
			await screen.findByRole("heading", { name: "No recipes yet" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
	});

	it("explains a missing catalog, and loads it once it exists", async () => {
		server.use(...noCatalog);
		const user = userEvent.setup();
		renderRecipes();

		expect(
			await screen.findByRole("heading", {
				name: "Recipe catalog unavailable",
			}),
		).toBeInTheDocument();
		server.resetHandlers();
		await user.click(screen.getByRole("button", { name: "Try again" }));

		expect(await screen.findByText("7 recipes")).toBeInTheDocument();
	});

	it("shows other failures as a route error", async () => {
		server.use(
			http.get("/api/v1/recipes", () =>
				HttpResponse.json({ message: "Boom" }, { status: 500 }),
			),
		);
		renderRecipes();

		expect(
			await screen.findByRole("heading", { name: "500 Internal Server Error" }),
		).toBeInTheDocument();
		expect(screen.getByText("Boom")).toBeInTheDocument();
	});

	it("shows each model's supported hardware as tags", async () => {
		renderRecipes();

		const nemotron = await card("Nemotron 5 Super 49B");
		expect(within(nemotron).getByText("DGX Station")).toBeInTheDocument();
		expect(within(nemotron).queryByText("DGX Spark")).not.toBeInTheDocument();
	});
});
