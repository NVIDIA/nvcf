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

import { Grid, Skeleton } from "@nvidia/foundations-react-core";
import { RecipeCardSkeleton } from "./components/RecipeCardSkeleton";
import { RecipesPageHeading } from "./components/RecipesPageHeading";

/** Loading state: the static heading, then skeletons shaped like the toolbar and the cards (slide 9). */
export function RecipesListPending() {
	return (
		<div aria-busy="true" className="flex flex-col gap-6">
			<RecipesPageHeading />
			<span className="sr-only">Loading recipes</span>
			<div className="flex flex-col gap-2 sm:flex-row">
				<Skeleton className="h-10 w-full sm:w-80" />
				<Skeleton className="h-10 w-full sm:w-44" />
			</div>
			<Grid colMinWidth="280px" gap="4">
				{["a", "b", "c", "d", "e", "f"].map((key) => (
					<RecipeCardSkeleton key={key} />
				))}
			</Grid>
		</div>
	);
}
