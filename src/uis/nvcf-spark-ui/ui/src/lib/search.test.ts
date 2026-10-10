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
import { parseSearch, stringifySearch, validateModelSearch } from "./search";

describe("search serialization", () => {
	it.each([
		"GLM-5.3-UD-IQ2_M",
		"meta/llama-3.1-8b-instruct",
		"1.0",
		"true",
		"null",
		'{"a":1}',
		"a b&c=d",
	])("round-trips %s as an opaque string", (model) => {
		expect(parseSearch(stringifySearch({ model }))).toEqual({ model });
	});

	it("omits undefined values", () => {
		expect(stringifySearch({ model: undefined })).toBe("");
	});

	it("parses with or without a leading question mark", () => {
		expect(parseSearch("?model=a%2Fb")).toEqual({ model: "a/b" });
		expect(parseSearch("model=a")).toEqual({ model: "a" });
	});
});

describe("validateModelSearch", () => {
	it("keeps a model name verbatim", () => {
		expect(
			validateModelSearch({ model: "meta/llama-3.1-8b-instruct" }),
		).toEqual({ model: "meta/llama-3.1-8b-instruct" });
	});

	it.each([
		["no model", {}],
		["an empty model", { model: "" }],
		["a non-string model", { model: 1 }],
	])("drops %s", (_, search) => {
		expect(validateModelSearch(search)).toEqual({});
	});
});
