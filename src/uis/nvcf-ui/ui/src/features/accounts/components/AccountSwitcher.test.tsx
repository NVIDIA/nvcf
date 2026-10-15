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

import { createRoute } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { getGetCloudAccountsMockHandler } from "~/generated/api/account/account.msw";
import type { AccountDto } from "~/generated/model/accountDto";
import { server } from "~/mocks/server";
import { rootRoute } from "~/rootRoute";
import { renderWithRouter } from "~/testing/render";

const STORAGE_KEY = "nvcf-active-nca-id";

function account(ncaId: string, name: string): AccountDto {
	return {
		ncaId,
		name,
		maxFunctionsAllowed: 0,
		maxTasksAllowed: 0,
		maxTelemetriesAllowed: 0,
		maxRegistryCredentialsAllowed: 0,
	};
}

// AccountSwitcher lives in the root AppShell, so any child route renders it.
const indexRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "/",
	component: () => null,
});

// An account-specific page that should send the user home on a switch.
const detailRoute = createRoute({
	getParentRoute: () => rootRoute,
	path: "/detail",
	staticData: { account: { redirectOnChange: "/" } },
	component: () => null,
});

beforeEach(() => {
	localStorage.clear();
	server.use(
		getGetCloudAccountsMockHandler({
			cloudAccounts: [account("nca-1", "NVIDIA"), account("nca-2", "Gaia")],
		}),
	);
});

describe("AccountSwitcher", () => {
	it("defaults to the first account when nothing is stored", async () => {
		renderWithRouter({ routes: [indexRoute], initialLocation: "/" });
		expect(
			await screen.findByRole("menuitemradio", { name: "NVIDIA" }),
		).toBeChecked();
	});

	it("reflects the stored account", async () => {
		localStorage.setItem(STORAGE_KEY, JSON.stringify("nca-2"));
		renderWithRouter({ routes: [indexRoute], initialLocation: "/" });
		expect(
			await screen.findByRole("menuitemradio", { name: "Gaia" }),
		).toBeChecked();
	});

	it("persists the selection when switched", async () => {
		const user = userEvent.setup();
		renderWithRouter({ routes: [indexRoute], initialLocation: "/" });

		await user.click(
			await screen.findByRole("menuitemradio", { name: "Gaia" }),
		);

		expect(localStorage.getItem(STORAGE_KEY)).toBe(JSON.stringify("nca-2"));
		expect(
			await screen.findByRole("menuitemradio", { name: "Gaia" }),
		).toBeChecked();
	});

	it("falls back to the first account when the stored id is stale", async () => {
		localStorage.setItem(STORAGE_KEY, JSON.stringify("Valisthea"));
		renderWithRouter({ routes: [indexRoute], initialLocation: "/" });
		expect(
			await screen.findByRole("menuitemradio", { name: "NVIDIA" }),
		).toBeChecked();
	});

	it("redirects when switching accounts on pages with accounts.redirectOnChange", async () => {
		const user = userEvent.setup();
		const { router } = renderWithRouter({
			routes: [indexRoute, detailRoute],
			initialLocation: "/detail",
		});

		await user.click(
			await screen.findByRole("menuitemradio", { name: "Gaia" }),
		);

		await waitFor(() => expect(router.state.location.pathname).toBe("/"));
	});
});
