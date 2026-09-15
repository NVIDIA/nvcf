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

import { Button, StatusMessage } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { FileQuestionIcon } from "lucide-react";

export function NotFound() {
	return (
		<div className="grid h-full place-items-center p-8">
			<StatusMessage
				size="medium"
				slotFooter={
					<Button asChild kind="secondary">
						<Link to="/" viewTransition>
							Go to dashboard
						</Link>
					</Button>
				}
				slotHeading="Page not found"
				slotMedia={<FileQuestionIcon />}
			/>
		</div>
	);
}
