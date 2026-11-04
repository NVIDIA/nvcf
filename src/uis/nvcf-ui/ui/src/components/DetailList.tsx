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

import { Button, Popover, Text } from "@nvidia/foundations-react-core";
import clsx from "clsx";
import { Info } from "lucide-react";
import type { ReactNode } from "react";

export type DetailListItem = {
	label: string;
	value?: ReactNode;
	tooltip?: ReactNode;
};

type DetailListProps = {
	className?: string;
	items: (DetailListItem | false)[];
};

function renderValue(value: ReactNode) {
	if (value === null || value === undefined)
		return <Text kind="body/regular/md">—</Text>;
	if (Array.isArray(value)) {
		const items = value.filter(
			(v): v is string | number =>
				typeof v === "string" || typeof v === "number",
		);
		if (items.length === 0) return <Text kind="body/regular/md">—</Text>;
		return (
			<div className="flex flex-col gap-1.5">
				{items.map((item) => (
					<Text className="break-all" key={item} kind="body/regular/md">
						{item}
					</Text>
				))}
			</div>
		);
	}
	if (typeof value === "string" || typeof value === "number") {
		return (
			<Text className="break-all" kind="body/regular/md">
				{value}
			</Text>
		);
	}
	return value;
}

export function DetailList({ className, items }: DetailListProps) {
	const resolved = items.filter((item): item is DetailListItem =>
		Boolean(item),
	);
	return (
		<div
			className={clsx(
				"grid grid-cols-1 sm:grid-cols-[165px_minmax(0,1fr)] sm:items-baseline gap-y-4 sm:gap-x-4",
				className,
			)}
		>
			{resolved.map(({ label, value, tooltip }) => (
				<div className="flex flex-col gap-1 sm:contents" key={label}>
					<div className="flex items-center gap-1">
						<Text className="text-secondary" kind="label/semibold/sm">
							{label}
						</Text>
						{tooltip && (
							<Popover slotContent={tooltip}>
								<Button
									aria-label={`More information about ${label}`}
									kind="tertiary"
									size="tiny"
								>
									<Info size="1em" />
								</Button>
							</Popover>
						)}
					</div>
					<div className="min-w-0">{renderValue(value)}</div>
				</div>
			))}
		</div>
	);
}
