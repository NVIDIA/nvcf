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

import { Button, StatusMessage, Text } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { UnplugIcon } from "lucide-react";

/**
 * Where an endpoint's "Metrics in Grafana" link lands when nothing on the
 * ingress serves Grafana: the request falls through to the UI's own catch-all.
 */
export function GrafanaUnavailable({ path }: { path: string }) {
	return (
		<div className="grid h-full place-items-center p-8">
			<StatusMessage
				size="medium"
				slotFooter={
					<div className="flex flex-col items-center gap-4">
						<Text
							className="max-w-xl text-center text-secondary [overflow-wrap:anywhere]"
							kind="body/regular/sm"
						>
							The link works again once the cluster serves Grafana at{" "}
							<Text asChild kind="mono/sm">
								<code className="rounded-sm bg-background-subtle px-1">
									{path}
								</code>
							</Text>
							.
						</Text>
						<Button asChild kind="secondary">
							<Link to="/registry" viewTransition>
								Go to endpoint registry
							</Link>
						</Button>
					</div>
				}
				slotHeading={<h1>Grafana unavailable</h1>}
				slotMedia={<UnplugIcon />}
				slotSubheading="Grafana isn't reachable on this cluster, so the dashboard can't open. The endpoints and the playground work without it."
			/>
		</div>
	);
}
