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

import { faker } from "@faker-js/faker";
import { getGetCloudAccountsResponseMock } from "~/generated/api/account/account.msw";
import { getGetClustersResponseMock } from "~/generated/api/clusters/clusters.msw";
import { getGetControlPlaneStatusResponseMock } from "~/generated/api/control-plane/control-plane.msw";
import { getGetFunctionDeploymentResponseMock } from "~/generated/api/function-deployment/function-deployment.msw";
import { getGetFunctionVersionResponseMock } from "~/generated/api/function-management/function-management.msw";
import type { AccountDto } from "~/generated/model/accountDto";
import type { DeploymentResponse } from "~/generated/model/deploymentResponse";
import type { FunctionDto } from "~/generated/model/functionDto";
import type { GetClusterResponse } from "~/generated/model/getClusterResponse";

/**
 * A seeded, referentially-consistent in-memory dataset that backs the default
 * mock handlers. Built once at module load — so unlike the raw generated mocks
 * (which re-roll random data on every request), list and detail views stay
 * stable and their IDs line up. Everything is keyed by account `ncaId`, which
 * is what every account-scoped endpoint resolves against.
 */

// Fixed seed so the dataset is reproducible across reloads, tests, and CI —
// same names/ids every run, which is what "seeded" implies.
faker.seed(1337);

const ACCOUNT_COUNT = 3;
const FUNCTIONS_PER_ACCOUNT = 8;
const CLUSTERS_PER_ACCOUNT = 6;

type AccountData = {
	/** keyed by `${functionId}/${versionId}` */
	functions: Map<string, FunctionDto>;
	/** keyed by functionId */
	deployments: Map<string, DeploymentResponse>;
	/** keyed by clusterId */
	clusters: Map<string, GetClusterResponse>;
};

function buildAccountData(ncaId: string): AccountData {
	const functions = new Map<string, FunctionDto>();
	const deployments = new Map<string, DeploymentResponse>();
	const clusters = new Map<string, GetClusterResponse>();

	for (let i = 0; i < FUNCTIONS_PER_ACCOUNT; i++) {
		const fn: FunctionDto = {
			...getGetFunctionVersionResponseMock().function,
			ncaId,
		};
		functions.set(`${fn.id}/${fn.versionId}`, fn);
		// Deployment stamped with the owning function's identity so the two agree.
		deployments.set(fn.id, {
			deployment: {
				...getGetFunctionDeploymentResponseMock().deployment,
				ncaId,
				functionId: fn.id,
				functionVersionId: fn.versionId,
				functionName: fn.name,
				functionStatus: fn.status,
			},
		});
	}

	for (const cluster of getGetClustersResponseMock().slice(
		0,
		CLUSTERS_PER_ACCOUNT,
	)) {
		const clusterId = cluster.clusterId ?? faker.string.uuid();
		clusters.set(clusterId, { ...cluster, clusterId, ncaId });
	}

	return { functions, deployments, clusters };
}

// Fixed account list with stable ncaIds; every other entity is seeded under one.
const cloudAccounts: AccountDto[] = Array.from(
	{ length: ACCOUNT_COUNT },
	() => {
		const [account] = getGetCloudAccountsResponseMock().cloudAccounts ?? [];
		return {
			...account,
			ncaId: faker.string.alphanumeric({ length: 13 }),
			name: faker.company.name(),
		};
	},
);

const accountData = new Map<string, AccountData>(
	cloudAccounts.map((a) => [a.ncaId, buildAccountData(a.ncaId)]),
);

// Control-plane status is global (not account-scoped) — seed a single snapshot.
const controlPlaneStatus = getGetControlPlaneStatusResponseMock();

export const mockStore = {
	cloudAccounts,
	controlPlaneStatus,
	account: (ncaId: string): AccountData | undefined => accountData.get(ncaId),
};
