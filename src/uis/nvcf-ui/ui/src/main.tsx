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
import { QueryClientProvider } from "@tanstack/react-query";
import { ReactQueryDevtools } from "@tanstack/react-query-devtools";
import { RouterProvider } from "@tanstack/react-router";
import ReactDOM from "react-dom/client";
import { queryClient } from "./lib/queryClient";
import { router } from "./router";

import "./styles.css";
import { getStoredTheme } from "./hooks/useThemePreference";

async function enableMocking() {
	// VITE_MOCK is a string env var — only the literal "true" enables mocking,
	// so VITE_MOCK="false" doesn't (a non-empty string is otherwise truthy).
	if (import.meta.env.VITE_MOCK !== "true") return;
	const { createWorker } = await import("./mocks/browser");
	const worker = await createWorker();
	return worker.start({
		onUnhandledRequest: "bypass",
		quiet: true,
		serviceWorker: { url: `${import.meta.env.BASE_URL}mockServiceWorker.js` },
	});
}

enableMocking()
	.catch((err) => console.error("[mock] Failed to start MSW worker:", err))
	.then(() => {
		const rootElement = document.getElementById("app");
		if (rootElement && !rootElement.innerHTML) {
			const root = ReactDOM.createRoot(rootElement);
			root.render(
				<ThemeProvider
					density="standard"
					global
					target="html"
					theme={getStoredTheme()}
				>
					<QueryClientProvider client={queryClient}>
						<RouterProvider context={{ queryClient }} router={router} />
						{import.meta.env.DEV && <ReactQueryDevtools />}
					</QueryClientProvider>
				</ThemeProvider>,
			);
		}
	});
