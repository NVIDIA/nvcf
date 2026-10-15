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

import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { readLocalStorage, useLocalStorage } from "./useLocalStorage";

const KEY = "test-key";

beforeEach(() => {
	localStorage.clear();
});

describe("readLocalStorage", () => {
	it("returns the fallback when nothing is stored", () => {
		expect(readLocalStorage(KEY, "fallback")).toBe("fallback");
	});

	it("deserializes a stored value", () => {
		localStorage.setItem(KEY, JSON.stringify({ a: 1 }));
		expect(readLocalStorage(KEY, null)).toEqual({ a: 1 });
	});

	it("returns a plain (non-JSON) string as-is", () => {
		localStorage.setItem(KEY, "plain-string");
		expect(readLocalStorage<string | null>(KEY, null)).toBe("plain-string");
	});
});

describe("useLocalStorage", () => {
	it("returns the fallback when nothing is stored", () => {
		const { result } = renderHook(() => useLocalStorage(KEY, "fallback"));
		expect(result.current[0]).toBe("fallback");
	});

	it("reads an existing stored value", () => {
		localStorage.setItem(KEY, JSON.stringify("stored"));
		const { result } = renderHook(() =>
			useLocalStorage<string | null>(KEY, null),
		);
		expect(result.current[0]).toBe("stored");
	});

	it("persists and re-renders with the new value when set", () => {
		const { result } = renderHook(() =>
			useLocalStorage<string | null>(KEY, null),
		);

		act(() => result.current[1]("next"));

		expect(result.current[0]).toBe("next");
		expect(localStorage.getItem(KEY)).toBe(JSON.stringify("next"));
	});

	it("syncs across hook instances in the same tab", () => {
		const a = renderHook(() => useLocalStorage<string | null>(KEY, null));
		const b = renderHook(() => useLocalStorage<string | null>(KEY, null));

		act(() => a.result.current[1]("shared"));

		expect(b.result.current[0]).toBe("shared");
	});
});
