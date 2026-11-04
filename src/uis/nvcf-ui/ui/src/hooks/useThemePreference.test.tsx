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

import { ThemeProvider } from "@nvidia/foundations-react-core";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { getStoredTheme, useThemePreference } from "./useThemePreference";

const STORAGE_KEY = "nvcf-theme";

function wrapper({ children }: { children: ReactNode }) {
	return (
		<ThemeProvider density="standard" theme="dark">
			{children}
		</ThemeProvider>
	);
}

beforeEach(() => {
	localStorage.clear();
});

describe("getStoredTheme", () => {
	it("returns 'dark' when localStorage is empty", () => {
		expect(getStoredTheme()).toBe("dark");
	});

	it.each(["light", "dark", "system"])("returns '%s' when stored", (theme) => {
		localStorage.setItem(STORAGE_KEY, theme);
		expect(getStoredTheme()).toBe(theme);
	});

	it("returns 'dark' when stored value is invalid", () => {
		localStorage.setItem(STORAGE_KEY, "invalid");
		expect(getStoredTheme()).toBe("dark");
	});
});

describe("useThemePreference", () => {
	it("returns the current theme from the provider", () => {
		const { result } = renderHook(() => useThemePreference(), { wrapper });
		expect(result.current.themePreference).toBe("dark");
	});

	it("updates provider and persists to localStorage", () => {
		const { result } = renderHook(() => useThemePreference(), { wrapper });

		act(() => {
			result.current.setThemePreference("system");
		});

		expect(result.current.themePreference).toBe("system");
		expect(localStorage.getItem(STORAGE_KEY)).toBe("system");
	});
});
