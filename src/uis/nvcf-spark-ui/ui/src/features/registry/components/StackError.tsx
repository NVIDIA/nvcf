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
import { AlertTriangleIcon, CloudOffIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useGetRegistry } from "~/generated/api/registry/registry";
import { errorMessage, HttpError } from "~/lib/fetch";
import { isUnreachable } from "../utils";

/**
 * Error state for routes that load the registry. The shell keeps polling the
 * registry, so the page reloads on its own as soon as a poll succeeds; at a
 * booth, nobody has to find the retry button after the stack comes back.
 */
export function StackError({ error }: ErrorComponentProps) {
	const router = useRouter();
	const { isSuccess } = useGetRegistry();
	// Only a recovery reloads: when the page failed for another reason while the
	// registry was fine, reloading would just fail again, in a loop.
	const [downAtMount] = useState(!isSuccess);
	useEffect(() => {
		if (downAtMount && isSuccess) void router.invalidate();
	}, [downAtMount, isSuccess, router]);

	const unreachable = isUnreachable(error);
	const detail =
		error instanceof HttpError ? errorMessage(error.body) : undefined;

	return (
		<div className="grid h-full place-items-center p-8">
			<StatusMessage
				size="medium"
				slotFooter={
					<Button kind="secondary" onClick={() => router.invalidate()}>
						Try again
					</Button>
				}
				slotHeading={
					<h1>
						{unreachable ? "Gateway unreachable" : "Couldn't load the registry"}
					</h1>
				}
				slotMedia={unreachable ? <CloudOffIcon /> : <AlertTriangleIcon />}
				slotSubheading={
					unreachable
						? "The LLM API Gateway isn't responding. This page reloads on its own once it's back."
						: (detail ?? (error instanceof Error ? error.message : undefined))
				}
			/>
		</div>
	);
}
