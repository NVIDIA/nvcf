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
	TableBody,
	TableDataCell,
	TableHead,
	TableHeaderCell,
	TableRoot,
	TableRow,
} from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import type { RegistryModel } from "~/generated/model/registryModel";
import { HealthStatus } from "./HealthStatus";

interface EndpointsTableProps {
	models: RegistryModel[];
	selected: string | undefined;
	/** Selects a model from a click anywhere on its row. */
	onSelect: (model: string) => void;
	/** Called when the model's link is followed, to reveal its detail. */
	onReveal: () => void;
}

/**
 * The registered models. Each model name is a link to its selection
 * (`?model=`), which keyboard and screen-reader users follow; the rest of the
 * row is a larger pointer target for the same selection.
 */
export function EndpointsTable({
	models,
	selected,
	onSelect,
	onReveal,
}: EndpointsTableProps) {
	return (
		// The same border and corner radius as the detail Panel beside it, as in
		// the mocks; the overflow clips the table to the rounded corners.
		<div className="w-full overflow-x-auto rounded-[var(--radius-density-xl)] border border-base">
			<TableRoot className="w-full" hoverableRows>
				<TableHead>
					<TableRow>
						<TableHeaderCell>Model</TableHeaderCell>
						<TableHeaderCell className="w-32 sm:w-36">Status</TableHeaderCell>
					</TableRow>
				</TableHead>
				<TableBody>
					{models.map(({ model, health }) => {
						const isSelected = model === selected;
						return (
							<TableRow
								className="cursor-pointer"
								key={model}
								onClick={(event) => {
									if (!(event.target as Element).closest("a")) onSelect(model);
								}}
								selected={isSelected}
							>
								<TableDataCell>
									<Link
										aria-current={isSelected ? "true" : undefined}
										className="block truncate text-body-bold-md text-primary no-underline focus-visible:underline"
										onClick={onReveal}
										replace
										resetScroll={false}
										search={{ model }}
										title={model}
										to="/registry"
									>
										{model}
									</Link>
								</TableDataCell>
								<TableDataCell>
									<HealthStatus health={health} />
								</TableDataCell>
							</TableRow>
						);
					})}
				</TableBody>
			</TableRoot>
		</div>
	);
}
