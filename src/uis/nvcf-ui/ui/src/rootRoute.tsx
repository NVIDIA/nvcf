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

import {
	AppBar,
	HorizontalNav,
	type HorizontalNavItem,
	SegmentedControl,
	Text,
	type Theme,
} from "@nvidia/foundations-react-core";
import type { QueryClient } from "@tanstack/react-query";
import {
	createRootRouteWithContext,
	HeadContent,
	Link,
	type LinkProps,
	Outlet,
	useMatchRoute,
} from "@tanstack/react-router";
import { TanStackRouterDevtools } from "@tanstack/react-router-devtools";
import { MonitorCog, Moon, Sun } from "lucide-react";
import { Suspense } from "react";

import { AccountSwitcher } from "./features/accounts/components/AccountSwitcher";
import { AccountSwitcherSkeleton } from "./features/accounts/components/AccountSwitcherSkeleton";
import { getGetCloudAccountsQueryOptions } from "./generated/api/account/account";
import { useThemePreference } from "./hooks/useThemePreference";

type NavItem = HorizontalNavItem & { href: LinkProps["to"] };

const navItems: NavItem[] = [
	{ value: "/", href: "/", children: "Dashboard" },
	{ value: "/clusters", href: "/clusters", children: "Compute Clusters" },
	{ value: "/functions", href: "/functions", children: "Functions" },
];

function AppShell() {
	const matchRoute = useMatchRoute();
	const { themePreference, setThemePreference } = useThemePreference();
	const activeValue = navItems.find((item) =>
		matchRoute({ to: item.href, fuzzy: item.value !== "/" }),
	)?.value;

	return (
		<>
			<HeadContent />
			<AppBar
				className="sticky top-0 z-50"
				slotEnd={
					<div className="flex gap-[inherit] items-center">
						<SegmentedControl
							defaultValue={themePreference}
							items={[
								{
									children: <MonitorCog size="1em" />,
									value: "system",
									"aria-label": "System Theme",
								},
								{
									children: <Sun size="1em" />,
									value: "light",
									"aria-label": "Light Theme",
								},
								{
									children: <Moon size="1em" />,
									value: "dark",
									"aria-label": "Dark Theme",
								},
							]}
							onValueChange={(value) => setThemePreference(value as Theme)}
							size="small"
						/>
						<Suspense fallback={<AccountSwitcherSkeleton />}>
							<AccountSwitcher />
						</Suspense>
					</div>
				}
				slotStart={<Text kind="label/bold/md">NVCF</Text>}
			>
				<HorizontalNav
					items={navItems}
					renderLink={(item) => (
						<Link {...item} to={item.href} viewTransition />
					)}
					value={activeValue}
				/>
			</AppBar>
			<main className="p-8">
				<Outlet />
			</main>
			{import.meta.env.DEV && <TanStackRouterDevtools />}
		</>
	);
}

export const rootRoute = createRootRouteWithContext<{
	queryClient: QueryClient;
}>()({
	component: AppShell,
	head: () => ({ meta: [{ title: "NVCF" }] }),
	beforeLoad: ({ context }) => {
		void context.queryClient.prefetchQuery(getGetCloudAccountsQueryOptions());
	},
});
