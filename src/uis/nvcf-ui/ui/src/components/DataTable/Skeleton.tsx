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
	Skeleton as KUISkeleton,
	TableBody,
	TableDataCell,
	TableHead,
	TableHeaderCell,
	TableRoot,
	TableRow,
} from "@nvidia/foundations-react-core";
import { useId } from "react";
import { Toolbar } from "./Toolbar";

const HEADER_WIDTHS = ["40%", "50%", "35%", "45%"];
const CELL_WIDTHS = ["70%", "50%", "60%", "80%", "55%", "75%", "65%", "45%"];

export function SkeletonToolbar() {
	return (
		<Toolbar>
			<KUISkeleton className="h-10 w-24 rounded-md" kind="pill" />
			<KUISkeleton className="h-4 w-24" kind="line" />
			<KUISkeleton className="h-10 flex-1 rounded-md" kind="line" />
			<KUISkeleton className="h-10 w-36 rounded-md" kind="pill" />
			<KUISkeleton className="h-10 w-28 rounded-md" kind="pill" />
		</Toolbar>
	);
}

interface SkeletonContentProps {
	rows?: number;
	columns?: number;
}

export function SkeletonContent({
	rows = 7,
	columns = 4,
}: SkeletonContentProps) {
	const id = useId();

	return (
		<TableRoot className="w-full bg-transparent" layout="fixed">
			<TableHead>
				<TableRow>
					{Array.from({ length: columns }, (_, i) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: static skeleton placeholders
						<TableHeaderCell key={`${id}-header-${i}`}>
							<div style={{ width: HEADER_WIDTHS[i % HEADER_WIDTHS.length] }}>
								<KUISkeleton kind="line" />
							</div>
						</TableHeaderCell>
					))}
				</TableRow>
			</TableHead>
			<TableBody>
				{Array.from({ length: rows }, (_, rowIndex) => (
					// biome-ignore lint/suspicious/noArrayIndexKey: static skeleton placeholders
					<TableRow key={`${id}-row-${rowIndex}`}>
						{Array.from({ length: columns }, (_, colIndex) => (
							// biome-ignore lint/suspicious/noArrayIndexKey: static skeleton placeholders
							<TableDataCell key={`${id}-cell-${rowIndex}-${colIndex}`}>
								<div
									style={{
										width:
											CELL_WIDTHS[(colIndex + rowIndex) % CELL_WIDTHS.length],
									}}
								>
									<KUISkeleton kind="line" />
								</div>
							</TableDataCell>
						))}
					</TableRow>
				))}
			</TableBody>
		</TableRoot>
	);
}

export function DataTableSkeleton({ rows, columns }: SkeletonContentProps) {
	return (
		<div className="flex flex-col gap-6">
			<SkeletonToolbar />
			<SkeletonContent columns={columns} rows={rows} />
		</div>
	);
}
