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

import { Card, Tag, Text } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { ArrowRightIcon } from "lucide-react";
import type { Deployment, RecipeModel } from "../utils";
import { RecipeStatusBadge } from "./RecipeStatusBadge";

/**
 * What the card says about a model's deployable builds: their precisions and
 * engines. Empty for a model with none, whose badge already says so.
 */
function buildSummary(model: RecipeModel): string {
	const deployable = model.builds.filter((b) => b.availability.deployable);
	const unique = (values: (string | null | undefined)[]) => [
		...new Set(values.filter((v): v is string => Boolean(v))),
	];
	return [
		unique(deployable.map((b) => b.precision)).join(", "),
		unique(deployable.map((b) => b.runtime?.version)).join(", "),
	]
		.filter(Boolean)
		.join(" · ");
}

/**
 * One model on the recipes page (slide 7): the whole card opens its recipe,
 * so it holds no other controls.
 */
export function RecipeCard({
	model,
	deployments,
}: {
	model: RecipeModel;
	deployments: readonly Deployment[];
}) {
	const summary = buildSummary(model);
	return (
		<Card asChild interactive>
			<Link
				className="h-full"
				params={{ recipeId: model.slug }}
				to="/recipes/$recipeId"
				viewTransition
			>
				<div className="flex h-full flex-col gap-4">
					<div className="flex flex-col items-start gap-1">
						<Text asChild kind="body/bold/xl">
							<h2 className="[overflow-wrap:anywhere]">{model.name}</h2>
						</Text>
						{/* Under the name on every card, so a long name never moves it. */}
						<RecipeStatusBadge deployments={deployments} model={model} />
						<Text
							className="line-clamp-2 text-secondary"
							kind="body/regular/sm"
						>
							{model.description ?? "No description in the catalog"}
						</Text>
					</div>
					<div className="flex flex-col gap-2">
						<Text className="text-secondary" kind="label/regular/sm">
							Supported on
						</Text>
						<div className="flex flex-wrap gap-2">
							{model.hardware.length > 0 ? (
								model.hardware.map((hardware) => (
									<Tag color="gray" key={hardware} kind="outline" readOnly>
										{hardware}
									</Tag>
								))
							) : (
								<Text className="text-secondary" kind="body/regular/sm">
									No validated hardware yet
								</Text>
							)}
						</div>
					</div>
					{summary ? (
						<Text className="text-placeholder" kind="label/regular/sm">
							{summary}
						</Text>
					) : null}
					<Text
						className="mt-auto inline-flex items-center gap-1 text-brand"
						kind="label/semibold/md"
					>
						Open recipe
						<ArrowRightIcon aria-hidden size="1em" />
					</Text>
				</div>
			</Link>
		</Card>
	);
}
