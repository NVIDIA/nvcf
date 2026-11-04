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

import type { HttpHandler } from "msw";
import { getGetCloudAccountsMockHandler } from "~/generated/api/account/account.msw";
import {
	getGetClusterForNcaIdAndClusterIdMockHandler,
	getGetClusterForNcaIdAndClusterIdResponseMock,
	getGetClustersMockHandler,
} from "~/generated/api/clusters/clusters.msw";
import { getGetControlPlaneStatusMockHandler } from "~/generated/api/control-plane/control-plane.msw";
import {
	getGetAllFunctionDeploymentsMockHandler,
	getGetFunctionDeploymentMockHandler,
	getGetFunctionDeploymentResponseMock,
} from "~/generated/api/function-deployment/function-deployment.msw";
import {
	getGetAllFunctionsMockHandler,
	getGetFunctionVersionMockHandler,
	getGetFunctionVersionResponseMock,
} from "~/generated/api/function-management/function-management.msw";
import { mockStore } from "./store";

/**
 * Store-backed default handlers. Each resolves the tenant bucket from
 * `info.params.ncaId` (and detail routes from their id params), falling back to
 * a freshly generated mock on a miss so an unknown id never 404s.
 */
export function getStoreHandlers(): HttpHandler[] {
	return [
		// Accounts — the seeded list; drives which ncaId everything else keys on.
		getGetCloudAccountsMockHandler({ cloudAccounts: mockStore.cloudAccounts }),

		// Functions
		getGetAllFunctionsMockHandler((info) => {
			const account = mockStore.account(info.params.ncaId as string);
			return { functions: [...(account?.functions.values() ?? [])] };
		}),
		getGetFunctionVersionMockHandler((info) => {
			const account = mockStore.account(info.params.ncaId as string);
			const key = `${info.params.functionId}/${info.params.functionVersionId}`;
			const fn = account?.functions.get(key);
			return fn ? { function: fn } : getGetFunctionVersionResponseMock();
		}),

		// Deployments
		getGetAllFunctionDeploymentsMockHandler((info) => {
			const account = mockStore.account(info.params.ncaId as string);
			return {
				deployments: [...(account?.deployments.values() ?? [])].map(
					(d) => d.deployment,
				),
			};
		}),
		getGetFunctionDeploymentMockHandler((info) => {
			const account = mockStore.account(info.params.ncaId as string);
			return (
				account?.deployments.get(info.params.functionId as string) ??
				getGetFunctionDeploymentResponseMock()
			);
		}),

		// Clusters
		getGetClustersMockHandler((info) => {
			const account = mockStore.account(info.params.ncaId as string);
			return [...(account?.clusters.values() ?? [])];
		}),
		getGetClusterForNcaIdAndClusterIdMockHandler((info) => {
			const account = mockStore.account(info.params.ncaId as string);
			return (
				account?.clusters.get(info.params.clusterId as string) ??
				getGetClusterForNcaIdAndClusterIdResponseMock()
			);
		}),

		// Control plane — global.
		getGetControlPlaneStatusMockHandler(mockStore.controlPlaneStatus),
	];
}
