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

import {
	Button,
	Grid,
	Select,
	StatusMessage,
	TextInput,
} from "@nvidia/foundations-react-core";
import { createLazyRoute, getRouteApi } from "@tanstack/react-router";
import { PackageOpenIcon, SearchIcon, SearchXIcon } from "lucide-react";
import { useCallback, useMemo } from "react";
import {
	asHardwareClass,
	HARDWARE_CLASSES,
} from "~/features/hardware/hardware";
import { useGetRecipesSuspense } from "~/generated/api/recipes/recipes";
import { RecipeCard } from "./components/RecipeCard";
import { RecipesPageHeading } from "./components/RecipesPageHeading";
import { useRegisteredModels } from "./hooks/useRegisteredModels";
import { useUrlText } from "./hooks/useUrlText";
import {
	deploymentsOf,
	groupRecipes,
	matchesHardware,
	matchesSearch,
} from "./utils";

export const RecipesListRoute = createLazyRoute("/recipes")({
	component: RecipesList,
});

const route = getRouteApi("/recipes");

function plural(count: number) {
	return count === 1 ? "recipe" : "recipes";
}

/**
 * The recipe catalog as cards, one per model (slide 7), narrowed by search and
 * hardware. Both live in the URL, so a filtered view can be shared.
 */
function RecipesList() {
	const { q = "", hardware } = route.useSearch();
	const navigate = route.useNavigate();
	const { data } = useGetRecipesSuspense();
	const registered = useRegisteredModels();
	const models = useMemo(() => groupRecipes(data.recipes), [data]);

	const writeQuery = useCallback(
		(value: string) => {
			void navigate({
				search: (prev) => ({ ...prev, q: value || undefined }),
				replace: true,
			});
		},
		[navigate],
	);
	const [query, setQuery] = useUrlText(q, writeQuery);

	const visible = models.filter(
		(m) => matchesSearch(m, query) && matchesHardware(m, hardware),
	);
	const searching = query.trim() !== "";
	const filtered = searching || hardware !== undefined;
	const reset = () => {
		setQuery("");
		void navigate({ search: {}, replace: true });
	};

	if (models.length === 0) {
		return (
			<div className="flex flex-col gap-6">
				<RecipesPageHeading />
				<div className="py-12">
					<StatusMessage
						slotHeading={<h2>No recipes yet</h2>}
						slotMedia={<PackageOpenIcon />}
						slotSubheading="This deployment's catalog lists no recipes. They show up here within a minute of being added to its ConfigMap."
					/>
				</div>
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-6">
			<RecipesPageHeading />
			<div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
				<TextInput
					aria-label="Search recipes"
					className="w-full sm:w-80"
					onValueChange={setQuery}
					placeholder="Search by name"
					slotStart={<SearchIcon aria-hidden size="1em" />}
					type="search"
					value={query}
				/>
				<Select
					aria-label="Filter by hardware"
					className="w-full sm:w-44"
					dismissible
					items={HARDWARE_CLASSES.map((h) => ({ value: h, children: h }))}
					onValueChange={(value: string) => {
						void navigate({
							search: (prev) => ({
								...prev,
								hardware: asHardwareClass(value),
							}),
							replace: true,
						});
					}}
					placeholder="Hardware"
					value={hardware ?? ""}
				/>
			</div>
			{/* Not shown: screen readers announce how many recipes a filter leaves. */}
			<output className="sr-only">
				{filtered
					? `${visible.length} of ${models.length} ${plural(models.length)}`
					: `${models.length} ${plural(models.length)}`}
			</output>
			{visible.length === 0 ? (
				<div className="py-12">
					<StatusMessage
						slotFooter={
							<Button kind="secondary" onClick={reset}>
								{searching && hardware
									? "Clear search and hardware"
									: searching
										? "Clear search"
										: "Show all hardware"}
							</Button>
						}
						slotHeading={<h2>No recipes match</h2>}
						slotMedia={<SearchXIcon />}
						slotSubheading={
							searching
								? `Nothing matches “${query.trim()}”${hardware ? ` on ${hardware}` : ""}.`
								: `No recipe supports ${hardware} yet.`
						}
					/>
				</div>
			) : (
				<Grid colMinWidth="280px" gap="4">
					{visible.map((model) => (
						<RecipeCard
							deployments={deploymentsOf(model, registered)}
							key={model.slug}
							model={model}
						/>
					))}
				</Grid>
			)}
		</div>
	);
}
