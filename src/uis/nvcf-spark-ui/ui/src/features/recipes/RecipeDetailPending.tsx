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

import { Skeleton } from "@nvidia/foundations-react-core";

/** Loading state: skeletons shaped like the header, the configurations and the steps. */
export function RecipeDetailPending() {
	return (
		<div aria-busy="true" className="flex flex-col gap-6">
			<span className="sr-only">Loading recipe</span>
			<div className="flex flex-col gap-3">
				<Skeleton className="h-4 w-56" />
				<Skeleton className="h-8 w-64" />
				<Skeleton className="h-4 w-full max-w-xl" />
			</div>
			<div className="grid grid-cols-[minmax(0,1fr)] items-start gap-8 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
				<div className="flex flex-col gap-3">
					<Skeleton className="h-6 w-48" />
					<Skeleton className="h-24 w-full" />
					<Skeleton className="h-24 w-full" />
				</div>
				<div className="flex flex-col gap-3">
					<Skeleton className="h-6 w-40" />
					<Skeleton className="h-64 w-full" />
					<Skeleton className="h-40 w-full" />
				</div>
			</div>
		</div>
	);
}
