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

import { describe, expect, it } from "vitest";
import { HttpError } from "~/lib/fetch";
import { StreamCutOffError, StreamError } from "~/lib/sse";
import { describeChatError } from "./utils";

function httpError(status: number, body: unknown = null, statusText = "") {
	return new HttpError(new Response(null, { status, statusText }), body);
}

describe("describeChatError", () => {
	it.each([
		["a cut-off stream", new StreamCutOffError(), "The response was cut off"],
		["a 401", httpError(401), "The gateway rejected this UI's key"],
		["a 502", httpError(502), "Gateway unreachable"],
		["a 504", httpError(504), "Gateway unreachable"],
		[
			"a network error",
			new TypeError("Failed to fetch"),
			"Gateway unreachable",
		],
		["a 529", httpError(529), "The model is busy"],
		[
			"an overloaded_error code",
			httpError(503, { error: { code: "overloaded_error", message: "busy" } }),
			"The model is busy",
		],
		[
			"model_not_found",
			httpError(404, { error: { code: "model_not_found", message: "gone" } }),
			"The model isn't routable",
		],
	])("names %s", (_, error, title) => {
		expect(describeChatError(error).title).toBe(title);
	});

	it("passes a mid-stream error's message through", () => {
		expect(describeChatError(new StreamError("CUDA out of memory"))).toEqual({
			title: "The model returned an error",
			detail: "CUDA out of memory",
		});
	});

	it("shows the gateway's message for other HTTP errors", () => {
		expect(
			describeChatError(
				httpError(413, { message: "Request Entity Too Large" }),
			),
		).toEqual({
			title: "The request failed",
			detail: "Request Entity Too Large",
		});
	});

	it("falls back to the status for an HTTP error without a message", () => {
		expect(
			describeChatError(httpError(500, null, "Internal Server Error")),
		).toEqual({
			title: "The request failed",
			detail: "The gateway answered 500 Internal Server Error.",
		});
	});

	it("describes anything else by its message", () => {
		expect(describeChatError(new Error("boom"))).toEqual({
			title: "Something went wrong",
			detail: "boom",
		});
		expect(describeChatError("weird")).toEqual({
			title: "Something went wrong",
			detail: "weird",
		});
	});
});
