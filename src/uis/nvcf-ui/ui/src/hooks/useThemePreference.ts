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

import { type Theme, useTheme } from "@nvidia/foundations-react-core";
import { useCallback } from "react";
import { flushSync } from "react-dom";

const STORAGE_KEY = "nvcf-theme";
const DEFAULT_THEME: Theme = "dark";

export function getStoredTheme(): Theme {
	const stored = localStorage.getItem(STORAGE_KEY) as Theme | null;
	if (stored === "light" || stored === "dark" || stored === "system") {
		return stored;
	}
	return DEFAULT_THEME;
}

function setStoredTheme(theme: Theme) {
	localStorage.setItem(STORAGE_KEY, theme);
}

export const useThemePreference = () => {
	const { unresolvedTheme, setTheme } = useTheme();
	const setThemePreference = useCallback(
		(theme: Theme) => {
			if (!document.startViewTransition) {
				setTheme(theme);
				setStoredTheme(theme);
			} /* v8 ignore start */ else {
				document.startViewTransition(() => {
					flushSync(() => {
						setTheme(theme);
						setStoredTheme(theme);
					});
				});
			} /* v8 ignore stop */
		},
		[setTheme],
	);

	return { themePreference: unresolvedTheme, setThemePreference };
};
