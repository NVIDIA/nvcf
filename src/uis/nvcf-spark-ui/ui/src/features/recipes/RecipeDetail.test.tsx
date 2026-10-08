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
import { describe, expect, it } from "vitest";
import { handlers as noCatalog } from "~/mocks/scenarios/recipes/unavailable";
import { server } from "~/mocks/server";
import { renderWithRouter } from "~/testing/render";
import { recipeRoute, recipesRoute } from "./routes";

function renderRecipe(location: string) {
	return renderWithRouter({
		routes: [recipesRoute, recipeRoute],
		initialLocation: location,
	});
}

function section(name: string) {
	const heading = screen.getByRole("heading", { level: 2, name });
	const element = heading.closest("section");
	if (!element) throw new Error(`no section ${name}`);
	return within(element);
}

/** The deployment steps' one snippet: the `helm install`. */
function helmCommand() {
	const buttons = section("Deployment steps").getAllByRole("button", {
		name: "Copy code",
	});
	expect(buttons).toHaveLength(1);
	return buttons[0]?.closest(".nv-code-snippet-root")?.textContent ?? "";
}

describe("RecipeDetail", () => {
	it("shows the model, its builds' facts, and the way back", async () => {
		renderRecipe("/recipes/qwen3.8-27b");

		expect(
			await screen.findByRole("heading", { level: 1, name: "Qwen3.8-27B" }),
		).toBeInTheDocument();
		expect(
			screen.getByText("Instruction-tuned, 27B parameters"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Served as qwen/qwen3.8-27b, qwen3.8-27b-nvfp4 · Runs on SGLang v0.5.19 · License: Apache-2.0",
			),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Model deployment recipes" }),
		).toHaveAttribute("href", "/recipes");
	});

	it("marks a deployed model and links to its endpoint", async () => {
		renderRecipe("/recipes/qwen3.8-27b");

		const link = await screen.findByRole("link", {
			name: "View qwen/qwen3.8-27b in the endpoint registry",
		});
		expect(link).toHaveAttribute("href", "/registry?model=qwen%2Fqwen3.8-27b");
		expect(screen.getByText("Deployed")).toBeInTheDocument();
	});

	it("offers every build and profile, the first picked", async () => {
		renderRecipe("/recipes/qwen3.8-27b");

		await screen.findByRole("heading", { level: 1, name: "Qwen3.8-27B" });
		const radios = section("Hardware configurations").getAllByRole("radio");
		expect(radios).toHaveLength(2);
		expect(radios[0]).toBeChecked();
		expect(radios[0]?.closest("label")).toHaveTextContent(
			"DGX Spark × 1FP8 · 104 GiB per node · up to 9,216 tokensTestedon 2026-10-07",
		);
		const helm = helmCommand();
		expect(helm).toContain(
			"helm install qwen3-8-27b oci://nvcr.io/org/pylon-sglang-recipe",
		);
		expect(helm).toContain("--set-string recipe=qwen3.8-27b");
		expect(helm).toContain("--set-string profileName=spark-fp8");
		expect(
			section("Deployment steps").queryByText(/OCIRepository/),
		).not.toBeInTheDocument();
	});

	it("rewrites the install command when another configuration is picked", async () => {
		const user = userEvent.setup();
		const { router } = renderRecipe("/recipes/qwen3.8-27b");

		await screen.findByRole("heading", { level: 1, name: "Qwen3.8-27B" });
		const [, nvfp4] = section("Hardware configurations").getAllByRole("radio");
		if (!nvfp4) throw new Error("no NVFP4 configuration");
		await user.click(nvfp4);

		await waitFor(() =>
			expect(router.state.location.search).toEqual({
				config: "qwen3.8-27b-nvfp4/spark-nvfp4",
			}),
		);
		expect(nvfp4).toBeChecked();
		const helm = helmCommand();
		expect(helm).toContain("helm install qwen3-8-27b-nvfp4");
		expect(helm).toContain("--set-string recipe=qwen3.8-27b-nvfp4");
		expect(helm).toContain("--set-string profileName=spark-nvfp4");
		expect(
			screen.getByRole("link", {
				name: "Find qwen3.8-27b-nvfp4 in the endpoint registry",
			}),
		).toHaveAttribute("href", "/registry?model=qwen3.8-27b-nvfp4");
	});

	it("opens a shared link on its configuration", async () => {
		renderRecipe(
			"/recipes/qwen3.8-flash-next?config=qwen3.8-flash-next%2Fspark-nvfp4-tp2",
		);

		await screen.findByRole("heading", {
			level: 1,
			name: "Qwen3.8-Flash-Next",
		});
		const [nvme, tp2] = section("Hardware configurations").getAllByRole(
			"radio",
		);
		expect(nvme).not.toBeChecked();
		expect(tp2).toBeChecked();
		expect(tp2?.closest("label")).toHaveTextContent("Not tested yet");
		const helm = helmCommand();
		expect(helm).toContain("--set-string 'nodes[1]=<node-2>'");
		expect(helm).toContain('"address":"<node-2-fabric-address>"');
		expect(helm).toContain("--set-json 'nodeCapabilities=");
		expect(
			section("Deployment steps").getByText("<fabric-…>, <link-gbps>"),
		).toBeInTheDocument();
	});

	it("falls back to the first configuration for an unknown one", async () => {
		renderRecipe("/recipes/qwen3.8-27b?config=gone%2Fgone");

		await screen.findByRole("heading", { level: 1, name: "Qwen3.8-27B" });
		expect(
			section("Hardware configurations").getAllByRole("radio")[0],
		).toBeChecked();
	});

	it("shows no validation status for a failed install", async () => {
		renderRecipe("/recipes/glm-5.3");

		await screen.findByRole("heading", { level: 1, name: "GLM-5.3" });
		expect(screen.queryByText(/fail/i)).not.toBeInTheDocument();
		const [glm] = section("Hardware configurations").getAllByRole("radio");
		expect(glm?.closest("label")).toHaveTextContent(
			"DGX Spark × 2UD-IQ2_M · 110 GiB per node · up to 2,048 tokens",
		);
		expect(glm?.closest("label")).not.toHaveTextContent("2026-10-07");
		expect(helmCommand()).toContain(
			"helm install glm-5-3 oci://nvcr.io/org/pylon-gguf-backend",
		);
	});

	it("explains why a model can't be deployed yet", async () => {
		renderRecipe("/recipes/nemotron-5-nano-12b");

		expect(
			await screen.findByRole("heading", { name: "Not available yet" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Exact-name public artifact and access status remain unverified.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Hardware configurations" }),
		).not.toBeInTheDocument();
		expect(screen.getByText("Not available")).toBeInTheDocument();
	});

	it("says when no recipe has that name", async () => {
		renderRecipe("/recipes/nope");

		expect(
			await screen.findByRole("heading", { name: "No recipe named nope" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Go to all recipes" }),
		).toHaveAttribute("href", "/recipes");
	});

	it("explains a missing catalog", async () => {
		server.use(...noCatalog);
		renderRecipe("/recipes/qwen3.8-27b");

		expect(
			await screen.findByRole("heading", {
				name: "Recipe catalog unavailable",
			}),
		).toBeInTheDocument();
	});
});
