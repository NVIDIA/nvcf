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
import { type ErrorComponentProps, useRouter } from "@tanstack/react-router";
import { AlertTriangleIcon } from "lucide-react";

export function RouteErrorFallback({ error }: ErrorComponentProps) {
	const router = useRouter();
	return (
		<div className="grid h-full place-items-center p-8">
			<StatusMessage
				size="medium"
				slotFooter={
					// `router.invalidate()` re-runs the route loader (and its queries) and
					// resets the error boundary; the boundary's own `reset` alone would
					// only re-render, not re-fetch loader data.
					<Button kind="secondary" onClick={() => router.invalidate()}>
						Try again
					</Button>
				}
				slotHeading={error.message || "Something went wrong"}
				slotMedia={<AlertTriangleIcon />}
			/>
		</div>
	);
}
