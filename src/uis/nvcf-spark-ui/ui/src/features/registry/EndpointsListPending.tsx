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
import { RegistryPageHeading } from "./components/RegistryPageHeading";

/** Loading state: the static heading, then skeletons shaped like the table and the detail panel. */
export function EndpointsListPending() {
	return (
		<div aria-busy="true" className="flex flex-col gap-6">
			<RegistryPageHeading />
			<div className="grid grid-cols-[minmax(0,1fr)] items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,28rem)] xl:grid-cols-[minmax(0,1fr)_minmax(0,34rem)]">
				<div className="flex flex-col gap-2">
					<span className="sr-only">Loading endpoints</span>
					<Skeleton className="h-10 w-full" />
					<Skeleton className="h-12 w-full" />
					<Skeleton className="h-12 w-full" />
					<Skeleton className="h-12 w-full" />
				</div>
				<Skeleton className="h-96 w-full" />
			</div>
		</div>
	);
}
