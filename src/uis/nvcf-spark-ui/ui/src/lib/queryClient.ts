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

import { QueryClient } from "@tanstack/react-query";

/**
 * How often the registry is polled (ms). The gateway refreshes its router
 * listing every 3 s, so polling faster buys nothing.
 */
export const REGISTRY_POLL_INTERVAL = 5_000;

export const queryClient = new QueryClient({
	defaultOptions: {
		queries: {
			staleTime: 2 * 60_000,
			refetchOnWindowFocus: true,
			retry: 1,
		},
	},
});
