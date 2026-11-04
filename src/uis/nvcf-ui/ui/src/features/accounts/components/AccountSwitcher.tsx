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

import { Avatar, Dropdown, Text } from "@nvidia/foundations-react-core";
import { useActiveAccount } from "../hooks/useActiveAccount";

export function AccountSwitcher() {
	const { accounts, activeAccount, switchAccount } = useActiveAccount();

	if (accounts.length === 0) {
		return null;
	}

	return (
		<Dropdown
			items={[
				{
					kind: "radio",
					radioKind: "check",
					slotHeading: null,
					name: "account",
					value: activeAccount?.ncaId,
					onValueChange: switchAccount,
					items: accounts.map((account) => ({
						value: account.ncaId,
						children: account.name,
					})),
				},
			]}
			size="small"
		>
			<div className="flex gap-2 items-center">
				<Avatar
					fallback={activeAccount?.name?.[0].toUpperCase()}
					interactive
					size="small"
				/>
				<Text className="truncate max-w-48" kind="label/regular/md">
					{activeAccount?.name}
				</Text>
			</div>
		</Dropdown>
	);
}
