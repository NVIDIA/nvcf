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

import { Anchor, Panel, Text } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { FunctionSquareIcon } from "lucide-react";
import { useGetAllFunctionsSuspense } from "~/generated/api/function-management/function-management";
import { FunctionDtoStatus } from "~/generated/model/functionDtoStatus";
import type { ListFunctionsResponse } from "~/generated/model/listFunctionsResponse";
import { formatNumberCompact } from "~/utils/formatters";
import { StatPanelBoundary } from "./StatPanelBoundary";

const functionsSelect = (data: ListFunctionsResponse) => {
	return {
		totalActive: (data.functions ?? []).filter(
			(f) => f.status === FunctionDtoStatus.ACTIVE,
		).length,
		total: data.functions?.length ?? 0,
	};
};

function FunctionStatsPanelContent({ ncaId }: { ncaId: string }) {
	const {
		data: { totalActive, total },
	} = useGetAllFunctionsSuspense(ncaId, undefined, {
		query: { select: functionsSelect },
	});

	return (
		<Panel
			elevation="high"
			slotHeading="Functions"
			slotIcon={<FunctionSquareIcon />}
		>
			<div className="flex flex-wrap items-baseline gap-x-6 gap-y-2">
				<div className="flex items-baseline gap-2">
					<Text className="text-brand" kind="display/md">
						{formatNumberCompact(totalActive)}
					</Text>
					<Anchor asChild>
						<Link
							search={{
								filters: [{ id: "status", value: [FunctionDtoStatus.ACTIVE] }],
							}}
							to="/functions"
							viewTransition
						>
							Active
						</Link>
					</Anchor>
				</div>
				<div className="flex items-baseline gap-2">
					<Text kind="display/md">{formatNumberCompact(total)}</Text>
					<Anchor asChild>
						<Link to="/functions" viewTransition>
							Total
						</Link>
					</Anchor>
				</div>
			</div>
		</Panel>
	);
}

export function FunctionStatsPanel({ ncaId }: { ncaId: string }) {
	return (
		<StatPanelBoundary
			skeletonCount={1}
			slotHeading="Functions"
			slotIcon={<FunctionSquareIcon />}
		>
			<FunctionStatsPanelContent ncaId={ncaId} />
		</StatPanelBoundary>
	);
}
