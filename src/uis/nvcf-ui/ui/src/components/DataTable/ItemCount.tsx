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

import { Text } from "@nvidia/foundations-react-core";
import { useDataTable } from "./DataTableContext";

interface ItemCountProps {
	label: string;
	pluralLabel?: string;
}

export function ItemCount({
	label,
	pluralLabel = `${label}s`,
}: ItemCountProps) {
	const table = useDataTable("ItemCount");
	const count = table.getRowCount();

	return (
		<Text className="whitespace-nowrap" kind="label/regular/md">
			{count} {count === 1 ? label : pluralLabel}
		</Text>
	);
}
