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

import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RegistryHealth } from "~/generated/model/registryHealth";
import { useHealthChanges } from "./useHealthChanges";

type Entry = { model: string; health: RegistryHealth };

const healthy: Entry = { model: "a", health: RegistryHealth.Healthy };
const unhealthy: Entry = { model: "a", health: RegistryHealth.Unhealthy };
const other: Entry = { model: "b", health: RegistryHealth.Healthy };

describe("useHealthChanges", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(Date.parse("2026-10-07T14:00:00Z"));
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	it("gives the first state seen no time", () => {
		const { result } = renderHook(() => useHealthChanges([healthy, other]));

		expect(result.current.size).toBe(0);
	});

	it("records when a model's health changes", () => {
		const { result, rerender } = renderHook(
			({ models }) => useHealthChanges(models),
			{ initialProps: { models: [healthy, other] } },
		);

		vi.setSystemTime(Date.parse("2026-10-07T14:03:12Z"));
		rerender({ models: [unhealthy, other] });

		expect(result.current.get("a")).toBe(Date.parse("2026-10-07T14:03:12Z"));
		expect(result.current.has("b")).toBe(false);
	});

	it("keeps the time through polls that change nothing", () => {
		const { result, rerender } = renderHook(
			({ models }) => useHealthChanges(models),
			{ initialProps: { models: [healthy] } },
		);
		vi.setSystemTime(Date.parse("2026-10-07T14:03:12Z"));
		rerender({ models: [unhealthy] });
		const changes = result.current;

		vi.setSystemTime(Date.parse("2026-10-07T14:09:00Z"));
		rerender({ models: [{ ...unhealthy }] });

		expect(result.current).toBe(changes);
		expect(result.current.get("a")).toBe(Date.parse("2026-10-07T14:03:12Z"));
	});

	it("moves the time when the health changes again", () => {
		const { result, rerender } = renderHook(
			({ models }) => useHealthChanges(models),
			{ initialProps: { models: [healthy] } },
		);
		rerender({ models: [unhealthy] });

		vi.setSystemTime(Date.parse("2026-10-07T14:20:00Z"));
		rerender({ models: [healthy] });

		expect(result.current.get("a")).toBe(Date.parse("2026-10-07T14:20:00Z"));
	});
});
