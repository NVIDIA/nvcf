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
	Anchor,
	Breadcrumbs,
	Button,
	PageHeader,
	RadioGroup,
	StatusMessage,
	Text,
} from "@nvidia/foundations-react-core";
import { createLazyRoute, getRouteApi, Link } from "@tanstack/react-router";
import { ArrowRightIcon, FileQuestionIcon, PackageXIcon } from "lucide-react";
import { useMemo } from "react";
import { useGetRecipesSuspense } from "~/generated/api/recipes/recipes";
import { ConfigurationTile } from "./components/ConfigurationTile";
import { DeploymentSteps } from "./components/DeploymentSteps";
import { RecipeStatusBadge } from "./components/RecipeStatusBadge";
import { useRegisteredModels } from "./hooks/useRegisteredModels";
import {
	configurations,
	deploymentsOf,
	groupRecipes,
	type RecipeModel,
} from "./utils";

export const RecipeDetailRoute = createLazyRoute("/recipes/$recipeId")({
	component: RecipeDetail,
});

const route = getRouteApi("/recipes/$recipeId");

function RecipesBreadcrumbs({ current }: { current: string }) {
	return (
		<Breadcrumbs
			items={[
				{ children: <Link to="/recipes">Model deployment recipes</Link> },
				current,
			]}
		/>
	);
}

/** Model names, engines and licenses across the model's deployable builds. */
function facts(model: RecipeModel): string {
	const builds = model.builds.filter((b) => b.availability.deployable);
	const unique = (values: (string | null | undefined)[]) => [
		...new Set(values.filter((v): v is string => Boolean(v))),
	];
	const names = unique(builds.map((b) => b.servedModelId));
	const engines = unique(builds.map((b) => b.runtime?.version));
	const licenses = unique(builds.map((b) => b.license));
	return [
		names.length > 0 ? `Served as ${names.join(", ")}` : undefined,
		engines.length > 0 ? `Runs on ${engines.join(", ")}` : undefined,
		licenses.length > 0 ? `License: ${licenses.join(", ")}` : undefined,
	]
		.filter(Boolean)
		.join(" · ");
}

/**
 * One model's recipe (slide 8): pick a hardware configuration, then deploy it
 * with the GitOps manifest or `helm install`. The pick lives in the URL
 * (`?config=`), so a link opens the same snippets.
 */
function RecipeDetail() {
	const { recipeId } = route.useParams();
	const { config: configKey } = route.useSearch();
	const navigate = route.useNavigate();
	const { data } = useGetRecipesSuspense();
	const registered = useRegisteredModels();
	const model = useMemo(
		() => groupRecipes(data.recipes).find((m) => m.slug === recipeId),
		[data, recipeId],
	);

	if (!model) {
		return (
			<div className="flex flex-col gap-6">
				<PageHeader
					kind="flat"
					slotBreadcrumbs={<RecipesBreadcrumbs current={recipeId} />}
					slotHeading={<h1>Recipe not found</h1>}
				/>
				<div className="py-12">
					<StatusMessage
						slotFooter={
							<Button asChild kind="secondary">
								<Link to="/recipes">Go to all recipes</Link>
							</Button>
						}
						slotHeading={<h2>No recipe named {recipeId}</h2>}
						slotMedia={<FileQuestionIcon />}
						slotSubheading="The catalog may have changed since this link was made."
					/>
				</div>
			</div>
		);
	}

	const configs = configurations(model);
	const selected = configs.find((c) => c.key === configKey) ?? configs[0];
	const deployments = deploymentsOf(model, registered);
	const unavailable = model.builds.find((b) => !b.availability.deployable);

	return (
		<div className="flex flex-col gap-6">
			<PageHeader
				kind="flat"
				slotBreadcrumbs={<RecipesBreadcrumbs current={model.name} />}
				slotDescription={model.description}
				slotHeading={
					<span className="flex flex-wrap items-center gap-3">
						<h1 className="[overflow-wrap:anywhere]">{model.name}</h1>
						<RecipeStatusBadge deployments={deployments} model={model} />
					</span>
				}
			>
				<div className="flex flex-col gap-1">
					<Text className="text-secondary" kind="body/regular/sm">
						{facts(model)}
					</Text>
					{deployments[0] ? (
						<Anchor
							asChild
							className="inline-flex items-center gap-1 pointer-coarse:min-h-11"
							kind="standalone"
						>
							<Link search={{ model: deployments[0].model }} to="/registry">
								View {deployments[0].model} in the endpoint registry
								<ArrowRightIcon aria-hidden size="1em" />
							</Link>
						</Anchor>
					) : null}
				</div>
			</PageHeader>

			{selected ? (
				<div className="grid grid-cols-[minmax(0,1fr)] items-start gap-8 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
					<section
						aria-labelledby="configurations-heading"
						className="flex min-w-0 flex-col gap-3"
					>
						<Text asChild kind="title/xs">
							<h2 id="configurations-heading">Hardware configurations</h2>
						</Text>
						<Text className="text-secondary" kind="body/regular/sm">
							Each is built and tested against its hardware. Pick one, and the
							deployment steps follow it.
						</Text>
						<RadioGroup
							aria-labelledby="configurations-heading"
							items={configs.map((config) => ({
								value: config.key,
								children: <ConfigurationTile config={config} />,
							}))}
							kind="tile"
							name="configuration"
							onValueChange={(key) => {
								void navigate({ search: { config: key }, replace: true });
							}}
							value={selected.key}
						/>
					</section>
					<section
						aria-labelledby="steps-heading"
						className="flex min-w-0 flex-col gap-3"
					>
						<Text asChild kind="title/xs">
							<h2 id="steps-heading">Deployment steps</h2>
						</Text>
						<DeploymentSteps config={selected} />
					</section>
				</div>
			) : (
				<div className="py-12">
					<StatusMessage
						slotHeading={<h2>Not available yet</h2>}
						slotMedia={<PackageXIcon />}
						slotSubheading={
							unavailable?.availability.reason ??
							"No build of this model can be deployed yet."
						}
					/>
				</div>
			)}
		</div>
	);
}
