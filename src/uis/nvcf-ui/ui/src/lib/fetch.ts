// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

export class HttpError extends Error {
	status: number;
	statusText: string;
	body: unknown;

	constructor(res: Response, body: unknown) {
		super(`${res.status} ${res.statusText}`);
		this.name = "HttpError";
		this.status = res.status;
		this.statusText = res.statusText;
		this.body = body;
	}
}

export async function customFetch<T>(
	url: string,
	options: RequestInit,
): Promise<T> {
	const res = await fetch(url, options);

	const body = [204, 205, 304].includes(res.status)
		? null
		: await res.json().catch(() => null);

	if (!res.ok) throw new HttpError(res, body);

	return body as T;
}
