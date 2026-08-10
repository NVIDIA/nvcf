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

import { Badge, type BadgeProps } from "@nvidia/foundations-react-core";
import {
	AlertTriangle,
	CircleCheck,
	CircleMinus,
	CircleX,
	type LucideIcon,
	RocketIcon,
	Trash2,
} from "lucide-react";
import type { FunctionDto } from "~/generated/model/functionDto";
import { FunctionDtoStatus } from "~/generated/model/functionDtoStatus";
import { toTitleCase } from "~/utils/formatters";

const statusConfig: Record<
	FunctionDto["status"],
	{ color: BadgeProps["color"]; icon: LucideIcon }
> = {
	[FunctionDtoStatus.ACTIVE]: { color: "green", icon: CircleCheck },
	[FunctionDtoStatus.DEPLOYING]: { color: "blue", icon: RocketIcon },
	[FunctionDtoStatus.ERROR]: { color: "red", icon: CircleX },
	[FunctionDtoStatus.INACTIVE]: { color: "gray", icon: CircleMinus },
	[FunctionDtoStatus.DELETED]: { color: "gray", icon: Trash2 },
	[FunctionDtoStatus.DEGRADED]: { color: "yellow", icon: AlertTriangle },
	[FunctionDtoStatus.DEGRADING]: { color: "yellow", icon: AlertTriangle },
};

const fallbackConfig: { color: BadgeProps["color"]; icon: LucideIcon } = {
	color: "gray",
	icon: CircleMinus,
};

export function StatusBadge({ status }: { status: FunctionDto["status"] }) {
	const { color, icon: Icon } = statusConfig[status] ?? fallbackConfig;
	return (
		<Badge color={color} kind="outline">
			<Icon size={12} />
			{toTitleCase(status)}
		</Badge>
	);
}
