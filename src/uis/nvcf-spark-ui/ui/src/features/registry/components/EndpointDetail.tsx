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
	Anchor,
	Button,
	Divider,
	Panel,
	Text,
} from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { ExternalLinkIcon } from "lucide-react";
import { useId } from "react";
import { CodeSnippet } from "~/components/CodeSnippet";
import { CopyButton } from "~/components/CopyButton";
import type { Config } from "~/generated/model/config";
import { RegistryHealth } from "~/generated/model/registryHealth";
import type { RegistryModel } from "~/generated/model/registryModel";
import { formatClock } from "~/utils/time";
import {
	CHAT_COMPLETIONS_PATH,
	chatCurl,
	gatewayApiBase,
	grafanaModelUrl,
} from "../utils";
import { HealthStatus } from "./HealthStatus";

interface EndpointDetailProps {
	endpoint: RegistryModel;
	/** When this page saw the endpoint's health last change (epoch ms), if it has. */
	healthSince: number | undefined;
	config: Config | undefined;
}

/** How to call one endpoint, and whether it can take traffic now. */
export function EndpointDetail({
	endpoint,
	healthSince,
	config,
}: EndpointDetailProps) {
	const { model, health } = endpoint;
	const headingId = useId();
	const reasonId = useId();
	const healthy = health === RegistryHealth.Healthy;
	const apiBase = gatewayApiBase(config?.gatewayUrl);
	const grafana = config?.grafanaUrl
		? grafanaModelUrl(config.grafanaUrl, model)
		: undefined;

	return (
		<section aria-labelledby={headingId}>
			<Panel
				slotFooter={
					<div className="flex w-full flex-wrap items-center justify-between gap-3">
						{grafana ? (
							<Anchor
								className="inline-flex items-center gap-1 pointer-coarse:min-h-11"
								href={grafana}
								kind="standalone"
								rel="noopener noreferrer"
								target="_blank"
							>
								Metrics in Grafana
								<ExternalLinkIcon aria-hidden size="1em" />
							</Anchor>
						) : (
							<span />
						)}
						{healthy ? (
							<Button
								asChild
								className="pointer-coarse:min-h-11"
								kind="secondary"
								size="small"
							>
								<Link
									rel="noopener"
									search={{ model }}
									target="_blank"
									to="/playground"
								>
									Try in playground
									<ExternalLinkIcon aria-hidden size="1em" />
								</Link>
							</Button>
						) : (
							<Button
								aria-describedby={reasonId}
								className="pointer-coarse:min-h-11"
								disabled
								kind="secondary"
								size="small"
							>
								Try in playground
							</Button>
						)}
					</div>
				}
			>
				<div className="flex flex-col gap-6">
					<div className="flex flex-col gap-2">
						<div className="flex min-w-0 items-center gap-2">
							<Text asChild className="min-w-0 truncate" kind="title/sm">
								<h2 id={headingId} title={model}>
									{model}
								</h2>
							</Text>
							<CopyButton ariaLabel="Copy model name" value={model} />
						</div>
						<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
							<HealthStatus health={health} />
							{/* The registry doesn't say how long a state has lasted,
							    so only a change this page saw has a time. */}
							{healthSince !== undefined && (
								<Text className="text-secondary" kind="label/regular/sm">
									since {formatClock(healthSince)}
								</Text>
							)}
						</div>
						{!healthy && (
							<Text
								className="text-secondary"
								id={reasonId}
								kind="body/regular/sm"
							>
								No healthy server is registered for this model, so the gateway
								can't route requests to it.
							</Text>
						)}
					</div>

					<Divider />

					<div className="flex flex-col gap-3">
						<Text asChild kind="label/bold/md">
							<h3>Request</h3>
						</Text>
						<CopyField
							copyLabel="Copy gateway URL"
							label="Gateway"
							value={apiBase}
						/>
						<CopyField
							copyLabel="Copy endpoint path"
							label="POST"
							value={CHAT_COMPLETIONS_PATH}
						/>
						<CodeSnippet language="bash" value={chatCurl(apiBase, model)} />
						<Text className="text-secondary" kind="body/regular/sm">
							Replace{" "}
							{!config?.gatewayUrl && (
								<>
									<Text kind="mono/sm">$GW_URL</Text> with the gateway's address
									and{" "}
								</>
							)}
							<Text kind="mono/sm">$GW_KEY</Text> with an API key from your
							gateway admin. This UI doesn't issue keys.
						</Text>
					</div>
				</div>
			</Panel>
		</section>
	);
}

/** A labeled single-line value with a copy action; long values truncate. */
function CopyField({
	label,
	value,
	copyLabel,
}: {
	label: string;
	value: string;
	copyLabel: string;
}) {
	return (
		<div className="flex min-w-0 items-center gap-3 rounded border border-base px-3 py-1">
			<Text className="shrink-0 text-secondary" kind="label/regular/xs">
				{label}
			</Text>
			<Text className="min-w-0 flex-1 truncate" kind="mono/sm" title={value}>
				{value}
			</Text>
			<CopyButton ariaLabel={copyLabel} value={value} />
		</div>
	);
}
