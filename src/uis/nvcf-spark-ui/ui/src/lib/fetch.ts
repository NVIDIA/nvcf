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

export class HttpError extends Error {
	status: number;
	statusText: string;
	body: unknown;

	constructor(res: Response, body: unknown) {
		super(`${res.status} ${res.statusText}`);
		this.name = "HttpError";
		this.status = res.status;
		this.statusText = res.statusText;
		this.body = body;
	}
}

export async function customFetch<T>(
	url: string,
	options: RequestInit,
): Promise<T> {
	const res = await fetch(url, options);

	if ([204, 205, 304].includes(res.status)) return null as T;

	if (!res.ok) {
		const body = await res.json().catch(() => null);
		throw new HttpError(res, body);
	}

	return res.json() as Promise<T>;
}

/**
 * The human-readable message of an error from the BFF or the gateway. Gateway
 * errors come in two shapes: the OpenAI error object (`{error: {message}}`)
 * for errors it raises itself, and `{message}` for errors it relays from the
 * router and for errors the BFF raises. Returns undefined when there is no
 * message to show.
 */
export function errorMessage(body: unknown): string | undefined {
	if (typeof body !== "object" || body === null) return undefined;
	if ("error" in body) {
		const { error } = body;
		if (typeof error === "object" && error !== null && "message" in error) {
			return typeof error.message === "string" && error.message
				? error.message
				: undefined;
		}
	}
	if ("message" in body && typeof body.message === "string" && body.message) {
		return body.message;
	}
	return undefined;
}

/** The OpenAI error `code` of a gateway error body, such as `overloaded_error`. */
export function errorCode(body: unknown): string | undefined {
	if (typeof body !== "object" || body === null || !("error" in body)) {
		return undefined;
	}
	const { error } = body;
	if (typeof error === "object" && error !== null && "code" in error) {
		return typeof error.code === "string" ? error.code : undefined;
	}
	return undefined;
}
