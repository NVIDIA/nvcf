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

import type { QueryClient } from "@tanstack/react-query";
import { getGetCloudAccountsQueryOptions } from "~/generated/api/account/account";
import { resolveActiveAccount } from "./hooks/useActiveAccount";

export async function getActiveNcaId(
	queryClient: QueryClient,
): Promise<string> {
	const response = await queryClient.ensureQueryData(
		getGetCloudAccountsQueryOptions(),
	);
	const account = resolveActiveAccount(response.cloudAccounts ?? []);
	if (!account) throw new Error("No account found");
	return account.ncaId;
}
