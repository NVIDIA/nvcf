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

import { type LinkProps, useRouter } from "@tanstack/react-router";
import { useCallback, useEffect } from "react";
import { useGetCloudAccountsSuspense } from "~/generated/api/account/account";
import type { AccountDto } from "~/generated/model/accountDto";
import { readLocalStorage, useLocalStorage } from "~/hooks/useLocalStorage";

const STORAGE_KEY = "nvcf-active-nca-id";

/**
 * Return active account from storage or first account in list if no stored value exists.
 */
export function resolveActiveAccount(
	accounts: AccountDto[],
): AccountDto | undefined {
	const storedNcaId = readLocalStorage<string | null>(STORAGE_KEY, null);
	return (
		accounts.find((account) => account.ncaId === storedNcaId) ?? accounts[0]
	);
}

export function useActiveAccount() {
	const router = useRouter();
	const { data } = useGetCloudAccountsSuspense();
	const accounts = data?.cloudAccounts ?? [];

	const [activeNcaId, setActiveNcaId] = useLocalStorage<string | null>(
		STORAGE_KEY,
		null,
	);
	const activeAccount =
		accounts.find((account) => account.ncaId === activeNcaId) ?? accounts[0];

	// Keep storage in sync: initialise on first load (null) and correct stale IDs.
	useEffect(() => {
		if (accounts.length > 0 && activeAccount?.ncaId !== activeNcaId) {
			setActiveNcaId(activeAccount?.ncaId ?? null);
		}
	}, [accounts, activeNcaId, activeAccount, setActiveNcaId]);

	const switchAccount = useCallback(
		async (ncaId: string) => {
			setActiveNcaId(ncaId);

			const redirectTo = router.state.matches.findLast(
				(match) => match.staticData.account?.redirectOnChange,
			)?.staticData.account?.redirectOnChange;

			if (redirectTo) {
				await router.navigate({
					to: redirectTo as LinkProps["to"],
					viewTransition: true,
				});
			} else {
				await router.invalidate();
			}
		},
		[router, setActiveNcaId],
	);

	return { accounts, activeAccount, switchAccount };
}
