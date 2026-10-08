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
import { PackageXIcon } from "lucide-react";
import { RouteErrorFallback } from "~/components/RouteErrorFallback";
import { HttpError } from "~/lib/fetch";

/**
 * The recipes pages' error state. A 503 means the deployment has no catalog
 * yet: it comes from a ConfigMap, which can be added or fixed without a
 * redeploy. Anything else is the generic route error.
 */
export function RecipesError(props: ErrorComponentProps) {
	const router = useRouter();
	if (!(props.error instanceof HttpError && props.error.status === 503)) {
		return <RouteErrorFallback {...props} />;
	}
	return (
		<div className="grid h-full place-items-center p-8">
			<StatusMessage
				size="medium"
				slotFooter={
					<Button kind="secondary" onClick={() => router.invalidate()}>
						Try again
					</Button>
				}
				slotHeading={<h1>Recipe catalog unavailable</h1>}
				slotMedia={<PackageXIcon />}
				slotSubheading="This deployment has no recipe catalog yet. It is read from a ConfigMap, so recipes show up within a minute of it being created or fixed."
			/>
		</div>
	);
}
