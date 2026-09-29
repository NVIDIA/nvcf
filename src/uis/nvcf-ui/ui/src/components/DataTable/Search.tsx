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

import { TextInput } from "@nvidia/foundations-react-core";
import { debounce } from "lodash-es";
import { Search as SearchIcon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useDataTable } from "./DataTableContext";

interface SearchProps {
	placeholder?: string;
	debounceMs?: number;
}

export function Search({
	placeholder = "Search...",
	debounceMs = 300,
}: SearchProps) {
	const table = useDataTable("Search");
	const [value, setValue] = useState(
		() => (table.getState().globalFilter as string) ?? "",
	);

	const setFilter = useMemo(
		() => debounce((v: string) => table.setGlobalFilter(v), debounceMs),
		[table, debounceMs],
	);

	useEffect(() => () => setFilter.cancel(), [setFilter]);

	return (
		<TextInput
			dismissible={value.length > 0}
			onDismiss={() => {
				setValue("");
				setFilter.cancel();
				table.setGlobalFilter("");
			}}
			onValueChange={(v) => {
				setValue(v);
				setFilter(v);
			}}
			placeholder={placeholder}
			slotStart={<SearchIcon size="1em" />}
			value={value}
		/>
	);
}
