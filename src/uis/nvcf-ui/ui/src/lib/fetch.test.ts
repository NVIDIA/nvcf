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

import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { server } from "~/mocks/server";
import { customFetch } from "./fetch";

const TEST_URL = "/test";

describe("customFetch", () => {
	it("returns parsed JSON on success", async () => {
		server.use(
			http.get(TEST_URL, () => HttpResponse.json({ id: 1, name: "test" })),
		);

		const data = await customFetch<{ id: number; name: string }>(TEST_URL, {
			method: "GET",
		});
		expect(data).toEqual({ id: 1, name: "test" });
	});

	it("throws HttpError with status and body on non-2xx response", async () => {
		server.use(
			http.get(TEST_URL, () =>
				HttpResponse.json({ message: "forbidden" }, { status: 403 }),
			),
		);

		await expect(
			customFetch(TEST_URL, { method: "GET" }),
		).rejects.toMatchObject({
			status: 403,
			body: { message: "forbidden" },
		});
	});

	it("returns null when response body is not valid JSON", async () => {
		server.use(
			http.get(TEST_URL, () => new HttpResponse("not json", { status: 200 })),
		);

		const data = await customFetch(TEST_URL, { method: "GET" });
		expect(data).toBeNull();
	});

	it("returns null for 204 No Content", async () => {
		server.use(
			http.delete(TEST_URL, () => new HttpResponse(null, { status: 204 })),
		);

		const data = await customFetch(TEST_URL, { method: "DELETE" });
		expect(data).toBeNull();
	});
});
