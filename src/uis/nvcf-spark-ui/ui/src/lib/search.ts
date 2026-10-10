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

/**
 * URL search (de)serialization for the router. The default JSON-decodes each
 * value, which would turn a model named `1.0` into the number 1. This app's
 * search params are opaque strings (model names), so they are read
 * and written verbatim.
 */
export function parseSearch(search: string): Record<string, string> {
	return Object.fromEntries(new URLSearchParams(search));
}

/**
 * `validateSearch` for routes that take `?model=`: an endpoint's model name,
 * kept verbatim. Anything else, or an empty value, means no model.
 */
export function validateModelSearch(search: Record<string, unknown>): {
	model?: string;
} {
	return typeof search.model === "string" && search.model !== ""
		? { model: search.model }
		: {};
}

export function stringifySearch(search: Record<string, unknown>): string {
	const params = new URLSearchParams();
	for (const [key, value] of Object.entries(search)) {
		if (value !== undefined && value !== null) params.set(key, String(value));
	}
	const query = params.toString();
	return query ? `?${query}` : "";
}
