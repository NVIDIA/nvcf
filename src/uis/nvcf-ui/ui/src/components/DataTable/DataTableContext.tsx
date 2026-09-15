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

import type { RowData, Table } from "@tanstack/react-table";
import { createContext, useContext } from "react";

interface DataTableContextValue {
	table: Table<RowData>;
}

const DataTableContext = createContext<DataTableContextValue | null>(null);

export const DataTableProvider = DataTableContext.Provider;

export function useDataTable(componentName: string) {
	const context = useContext(DataTableContext);
	if (!context) {
		throw new Error(`${componentName} must be used within a DataTable`);
	}
	return context.table;
}
