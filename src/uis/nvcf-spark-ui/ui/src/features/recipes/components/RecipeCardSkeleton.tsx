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

import { Card, Skeleton } from "@nvidia/foundations-react-core";

/** A card-shaped placeholder: title, description, supported-hardware row and action (slide 9). */
export function RecipeCardSkeleton() {
	return (
		<Card>
			<div className="flex flex-col gap-4">
				<div className="flex flex-col gap-2">
					<Skeleton className="h-6 w-2/3" />
					<Skeleton className="h-4 w-full" />
				</div>
				<div className="flex flex-col gap-2">
					<Skeleton className="h-4 w-24" />
					<div className="flex gap-2">
						<Skeleton className="h-6 w-20" kind="pill" />
						<Skeleton className="h-6 w-24" kind="pill" />
					</div>
				</div>
				<Skeleton className="h-4 w-1/2" />
				<Skeleton className="h-5 w-28" />
			</div>
		</Card>
	);
}
