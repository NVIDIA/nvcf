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

import { isUnreachable } from "~/features/registry/utils";
import { errorCode, errorMessage, HttpError } from "~/lib/fetch";
import { StreamCutOffError, StreamError } from "~/lib/sse";

/** Prefilled in an empty chat, so a booth visitor can send right away. */
export const EXAMPLE_PROMPT =
	"Explain the difference between latency and throughput when serving a model.";

export interface ChatErrorInfo {
	title: string;
	detail: string;
}

/** What went wrong with a chat request, in words a booth visitor can act on. */
export function describeChatError(error: unknown): ChatErrorInfo {
	if (error instanceof StreamCutOffError) {
		return {
			title: "The response was cut off",
			detail: "The connection to the model closed before the answer finished.",
		};
	}
	if (error instanceof StreamError) {
		return { title: "The model returned an error", detail: error.message };
	}
	if (isUnreachable(error)) {
		return {
			title: "Gateway unreachable",
			detail: "The request didn't reach the model. Try again in a moment.",
		};
	}
	if (error instanceof HttpError) {
		if (error.status === 401) {
			return {
				title: "The gateway rejected this UI's key",
				detail: "Ask the operator to check the demo-ui API key.",
			};
		}
		if (error.status === 529 || errorCode(error.body) === "overloaded_error") {
			return {
				title: "The model is busy",
				detail: "It's serving another request. Try again in a moment.",
			};
		}
		if (errorCode(error.body) === "model_not_found") {
			return {
				title: "The model isn't routable",
				detail:
					"It stopped or became unhealthy. Check its status in the endpoint registry.",
			};
		}
		return {
			title: "The request failed",
			detail:
				errorMessage(error.body) ??
				`The gateway answered ${error.status} ${error.statusText}.`.trim(),
		};
	}
	return {
		title: "Something went wrong",
		detail: error instanceof Error ? error.message : String(error),
	};
}
