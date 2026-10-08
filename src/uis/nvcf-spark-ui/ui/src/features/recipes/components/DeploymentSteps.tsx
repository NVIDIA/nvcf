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

import { Anchor, Text } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { ArrowRightIcon } from "lucide-react";
import type { ReactNode } from "react";
import { CodeSnippet } from "~/components/CodeSnippet";
import {
	formatGiB,
	helmInstallCommand,
	type RecipeConfiguration,
} from "../utils";

/** What each placeholder in the command stands for, for this configuration. */
function placeholders(config: RecipeConfiguration): [string, string][] {
	const { profile, nodeCount } = config;
	const system = config.hardware ?? "matching";
	const cache = profile.perNode[0]?.storage?.claimRequestBytes;
	const nvme = profile.perNode.some(
		(node) => node.storage?.offloadMedium === "local-nvme",
	);
	const rows: [string, string][] = [
		[
			nodeCount > 1 ? `<node-1> … <node-${nodeCount}>` : "<node-1>",
			`${nodeCount > 1 ? `${nodeCount} distinct nodes, each` : "A node"} with a free ${system} GPU${nvme ? " and a local NVMe drive" : ""}.`,
		],
		["<runtime-class>", "The RuntimeClass of the NVIDIA container runtime."],
		[
			"<storage-class>",
			`A StorageClass for the model cache${cache === undefined ? "" : `, ${formatGiB(cache)} per node`}.`,
		],
	];
	if (profile.fabric) {
		rows.push([
			"<fabric-…>, <link-gbps>",
			`Each node's link on the node-to-node fabric${profile.fabric.minimumGbps === undefined ? "" : `, at least ${profile.fabric.minimumGbps} Gb/s`}.`,
		]);
	}
	if (profile.deployment.chart.repository === undefined) {
		rows.push([
			"<chart-registry>",
			"The OCI registry the recipe charts are published to.",
		]);
	}
	return rows;
}

function Step({
	number,
	title,
	children,
}: {
	number: string;
	title: string;
	children: ReactNode;
}) {
	return (
		<li className="flex min-w-0 flex-col gap-3">
			<div className="flex items-baseline gap-3">
				<Text aria-hidden className="text-secondary" kind="mono/sm">
					{number}
				</Text>
				<Text kind="body/regular/md">{title}</Text>
			</div>
			{children}
		</li>
	);
}

/**
 * How to deploy the picked configuration (slide 8, without its GitOps
 * manifest): the `helm install`, which follows the configuration, then what
 * happens next.
 */
export function DeploymentSteps({ config }: { config: RecipeConfiguration }) {
	const { build } = config;
	return (
		<div className="flex min-w-0 flex-col gap-6">
			<div className="flex flex-col gap-2">
				<Text className="text-secondary" kind="body/regular/sm">
					Replace the values in angle brackets with your cluster's:
				</Text>
				<dl className="grid grid-cols-[minmax(0,1fr)] gap-x-4 gap-y-1 sm:grid-cols-[auto_minmax(0,1fr)]">
					{placeholders(config).map(([term, meaning]) => (
						<div className="contents" key={term}>
							<Text asChild kind="mono/sm">
								<dt className="[overflow-wrap:anywhere]">{term}</dt>
							</Text>
							<Text asChild className="text-secondary" kind="body/regular/sm">
								<dd className="mb-2 sm:mb-0">{meaning}</dd>
							</Text>
						</div>
					))}
				</dl>
			</div>
			<ol className="flex flex-col gap-6">
				<Step number="01" title="Install the recipe's chart with Helm.">
					<CodeSnippet language="bash" value={helmInstallCommand(config)} />
				</Step>
				<Step
					number="02"
					title="Once its model has loaded, the endpoint registers itself with the gateway. It then appears in the endpoint registry, and the playground can chat with it."
				>
					{build.servedModelId ? (
						<Anchor
							asChild
							className="inline-flex items-center gap-1 pointer-coarse:min-h-11"
							kind="standalone"
						>
							<Link search={{ model: build.servedModelId }} to="/registry">
								Find {build.servedModelId} in the endpoint registry
								<ArrowRightIcon aria-hidden size="1em" />
							</Link>
						</Anchor>
					) : null}
				</Step>
			</ol>
		</div>
	);
}
