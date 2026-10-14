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

import { StatusIndicator, Text } from "@nvidia/foundations-react-core";
import { useGetConfig } from "~/generated/api/config/config";
import { useGetRegistry } from "~/generated/api/registry/registry";
import { REGISTRY_POLL_INTERVAL } from "~/lib/queryClient";
import { gatewayHost, type StackStatus, stackStatus } from "../utils";

// `short` is for phones, where the AppBar has room for little beside the brand.
const display: Record<
	StackStatus,
	{ label: string; short: string; color: "blue" | "green" | "red" | "yellow" }
> = {
	connecting: { label: "Connecting…", short: "Connecting…", color: "blue" },
	connected: { label: "Gateway connected", short: "Connected", color: "green" },
	unreachable: {
		label: "Gateway unreachable",
		short: "Unreachable",
		color: "red",
	},
	error: { label: "Gateway error", short: "Error", color: "yellow" },
};

/**
 * Routing-stack health for the app shell, from the registry query.
 *
 * This is the app's only registry poller: the shell is on every page, and
 * views read the same cache entry without polling themselves, so each open
 * browser sends one registry request per interval.
 */
export function GatewayStatus() {
	const registry = useGetRegistry(undefined, {
		query: { refetchInterval: REGISTRY_POLL_INTERVAL },
	});
	const { data: config } = useGetConfig();
	const { label, short, color } =
		display[stackStatus(registry.error, registry.data !== undefined)];
	const host = gatewayHost(config?.gatewayUrl);

	return (
		<output className="flex min-w-0 items-center gap-2">
			<StatusIndicator color={color} size="small" />
			<Text className="whitespace-nowrap sm:hidden" kind="label/regular/md">
				{short}
			</Text>
			<Text
				className="hidden whitespace-nowrap sm:inline"
				kind="label/regular/md"
			>
				{label}
			</Text>
			{host && (
				<Text
					className="hidden truncate text-secondary md:inline"
					kind="mono/sm"
				>
					{host}
				</Text>
			)}
		</output>
	);
}
