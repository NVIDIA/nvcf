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
import { getGetRecipesQueryOptions } from "~/generated/api/recipes/recipes";
import { rootRoute } from "~/rootRoute";
import { RecipesError } from "./components/RecipesError";
import { RecipeDetailPending } from "./RecipeDetailPending";
import { RecipesListPending } from "./RecipesListPending";
import {
	groupRecipes,
	validateRecipeSearch,
	validateRecipesSearch,
} from "./utils";

export const recipesRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "recipes",
	validateSearch: validateRecipesSearch,
	head: () => ({
		meta: [{ title: "Model deployment recipes · NVCF Gateway" }],
	}),
	pendingComponent: RecipesListPending,
	errorComponent: RecipesError,
	// The catalog is the only data either page waits for: deployed markers come
	// from the registry the shell polls, so a stack outage doesn't fail them.
	loader: ({ context: { queryClient } }) =>
		queryClient.ensureQueryData({
			...getGetRecipesQueryOptions(),
			revalidateIfStale: true,
		}),
}).lazy(() => import("./RecipesList").then((m) => m.RecipesListRoute));

export const recipeRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "recipes/$recipeId",
	validateSearch: validateRecipeSearch,
	pendingComponent: RecipeDetailPending,
	errorComponent: RecipesError,
	loader: ({ context: { queryClient } }) =>
		queryClient.ensureQueryData({
			...getGetRecipesQueryOptions(),
			revalidateIfStale: true,
		}),
	// After `loader`, so its data type is inferred first.
	head: ({ loaderData, params }) => {
		const name = loaderData
			? groupRecipes(loaderData.recipes).find((m) => m.slug === params.recipeId)
					?.name
			: undefined;
		return {
			meta: [
				{
					title: `${name ?? "Recipe"} · Model deployment recipes · NVCF Gateway`,
				},
			],
		};
	},
}).lazy(() => import("./RecipeDetail").then((m) => m.RecipeDetailRoute));
