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
	CircleCheck,
	CircleMinus,
	CirclePause,
	CircleX,
	Trash2,
	Wrench,
} from "lucide-react";
import type { ComponentType } from "react";
import type { GetClusterResponse } from "~/generated/model/getClusterResponse";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { toTitleCase } from "~/utils/formatters";

type StatusConfig = {
	color: BadgeProps["color"];
	icon: ComponentType<{ size?: number }>;
	label?: string;
};

const statusConfig: Record<
	NonNullable<GetClusterResponse["status"]>,
	StatusConfig
> = {
	[GetClusterResponseStatus.READY]: { color: "green", icon: CircleCheck },
	[GetClusterResponseStatus.PAUSED]: { color: "blue", icon: CirclePause },
	[GetClusterResponseStatus.FAILED]: { color: "red", icon: CircleX },
	[GetClusterResponseStatus.ABANDONED]: { color: "red", icon: CircleX },
	[GetClusterResponseStatus.NOT_READY]: { color: "gray", icon: CircleMinus },
	[GetClusterResponseStatus.UNHEALTHY]: {
		color: "red",
		icon: CircleX,
		label: "Error",
	},
	[GetClusterResponseStatus.DELETED]: { color: "gray", icon: Trash2 },
	[GetClusterResponseStatus.CORDON]: { color: "purple", icon: Wrench },
	[GetClusterResponseStatus.CORDON_AND_DRAIN]: {
		color: "purple",
		icon: Wrench,
	},
};

const fallbackConfig: StatusConfig = { color: "gray", icon: CircleMinus };

export function getStatusLabel(
	status: NonNullable<GetClusterResponse["status"]>,
) {
	return (statusConfig[status] ?? fallbackConfig).label ?? toTitleCase(status);
}

export function StatusBadge({
	status = GetClusterResponseStatus.NOT_READY,
}: {
	status?: GetClusterResponse["status"];
}) {
	const { color, icon: Icon } = statusConfig[status] ?? fallbackConfig;
	return (
		<Badge color={color} kind="outline">
			<Icon size={12} />
			{getStatusLabel(status)}
		</Badge>
	);
}
