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
	Button,
	Panel,
	StatusMessage,
	Text,
} from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { ServerIcon } from "lucide-react";

/** First-use state: nothing has registered with the gateway (slide 6). */
export function RegistryEmpty() {
	return (
		<Panel>
			<div className="py-12">
				<StatusMessage
					slotFooter={
						<div className="flex flex-col items-center gap-4">
							<Text
								className="max-w-xl text-center text-secondary"
								kind="body/regular/sm"
							>
								The registry is held in memory, so a gateway restart clears it
								until the endpoints register again. Nothing is wrong here.
							</Text>
							<Button asChild kind="secondary">
								<Link to="/recipes">Browse recipes</Link>
							</Button>
						</div>
					}
					slotHeading={<h2>Nothing is registered yet</h2>}
					slotMedia={<ServerIcon />}
					slotSubheading="The gateway discovers endpoints as they register themselves. Deploy a recipe from your Git repository and it appears here within seconds."
				/>
			</div>
		</Panel>
	);
}
