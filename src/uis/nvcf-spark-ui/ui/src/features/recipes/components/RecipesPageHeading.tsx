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

import { PageHeader } from "@nvidia/foundations-react-core";

/** The recipes list's heading, static so its loading state can show it at once. */
export function RecipesPageHeading() {
	return (
		<PageHeader
			kind="flat"
			slotDescription="Pre-optimized inference containers, each tested against specific hardware."
			slotHeading={<h1>Model deployment recipes</h1>}
		/>
	);
}
