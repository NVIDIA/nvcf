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
	Panel,
	type PanelProps,
	Skeleton,
} from "@nvidia/foundations-react-core";

export function StatPanelSkeleton({
	count = 2,
	slotHeading,
	slotIcon,
}: { count?: number } & PanelProps) {
	return (
		<Panel
			elevation="high"
			slotHeading={
				slotHeading ?? <Skeleton className="h-[18px] w-44" kind="line" />
			}
			slotIcon={slotIcon ?? <Skeleton className="size-6" kind="circle" />}
		>
			<div className="flex flex-wrap items-baseline gap-x-6 gap-y-2">
				{Array.from({ length: count }, (_, i) => (
					// biome-ignore lint/suspicious/noArrayIndexKey: static skeleton list, no reordering
					<div className="flex items-end gap-2" key={i}>
						<Skeleton className="size-12 my-[3.5px]" kind="circle" />
						<Skeleton className="h-[21px] w-12" kind="line" />
					</div>
				))}
			</div>
		</Panel>
	);
}
