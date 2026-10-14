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

import { HttpResponse, http } from "msw";

/**
 * Every server for the model is busy. GLM runs one request at a time
 * (`maxEngineConcurrency: 1`), so a second visitor sees this.
 */
export const handlers = [
	http.post("/v1/chat/completions", () =>
		HttpResponse.json(
			{
				error: {
					code: "overloaded_error",
					message: "Inference capacity is temporarily unavailable.",
					param: "",
					type: "overloaded_error",
				},
			},
			{ status: 529 },
		),
	),
];
