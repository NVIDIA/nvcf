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
	Anchor,
	AppBar,
	AppBarExpanderButton,
	HorizontalNav,
	type HorizontalNavItem,
	SegmentedControl,
	SidePanel,
	type Theme,
	VerticalNav,
} from "@nvidia/foundations-react-core";
import type { QueryClient } from "@tanstack/react-query";
import {
	createRootRouteWithContext,
	HeadContent,
	Link,
	type LinkProps,
	Outlet,
	useLocation,
	useMatchRoute,
} from "@tanstack/react-router";
import { TanStackRouterDevtools } from "@tanstack/react-router-devtools";
import { MonitorCog, Moon, Sun } from "lucide-react";
import { useState } from "react";
import { NotFound } from "~/components/NotFound";
import { GatewayStatus } from "~/features/registry/components/GatewayStatus";
import { GrafanaUnavailable } from "~/features/registry/components/GrafanaUnavailable";
import { isGrafanaPath } from "~/features/registry/utils";
import {
	getGetConfigQueryOptions,
	useGetConfig,
} from "~/generated/api/config/config";
import { useThemePreference } from "~/hooks/useThemePreference";
import { showDevtools } from "~/lib/devtools";

type NavItem = HorizontalNavItem & { href: LinkProps["to"] };

// The playground has no nav item: it opens in its own tab from an endpoint's
// "Try in playground" action.
const navItems: NavItem[] = [
	{ value: "/registry", href: "/registry", children: "Endpoint registry" },
	{
		value: "/recipes",
		href: "/recipes",
		children: "Model deployment recipes",
	},
];

function AppShell() {
	const matchRoute = useMatchRoute();
	const { themePreference, setThemePreference } = useThemePreference();
	const [navOpen, setNavOpen] = useState(false);
	const activeValue = navItems.find((item) =>
		matchRoute({ to: item.href, fuzzy: true }),
	)?.value;

	return (
		<>
			<HeadContent />
			{/* Below sm the nav collapses behind the expander, into this drawer. */}
			<SidePanel
				modal
				onOpenChange={setNavOpen}
				open={navOpen}
				side="left"
				slotHeading="Navigation"
			>
				<VerticalNav
					className="w-full border-r-0"
					items={navItems.map((item) => ({
						id: item.value,
						href: item.href ?? "/",
						children: item.children,
						active: item.value === activeValue,
					}))}
					renderLink={({ href, children }) => (
						<Link onClick={() => setNavOpen(false)} to={href}>
							{children}
						</Link>
					)}
				/>
			</SidePanel>
			<AppBar
				className="sticky top-0 z-50"
				slotEnd={
					<>
						<GatewayStatus />
						<SegmentedControl
							className="hidden sm:flex"
							defaultValue={themePreference}
							items={[
								{
									children: <MonitorCog size="1em" />,
									value: "system",
									"aria-label": "System theme",
								},
								{
									children: <Sun size="1em" />,
									value: "light",
									"aria-label": "Light theme",
								},
								{
									children: <Moon size="1em" />,
									value: "dark",
									"aria-label": "Dark theme",
								},
							]}
							onValueChange={(value) => setThemePreference(value as Theme)}
							size="small"
						/>
					</>
				}
				slotStart={
					<>
						<AppBarExpanderButton
							aria-expanded={navOpen}
							aria-label="Open navigation"
							className="pointer-coarse:min-h-11 pointer-coarse:min-w-11 sm:hidden"
							onClick={() => setNavOpen(true)}
						/>
						<Anchor asChild kind="standalone" textKind="inherit">
							<Link className="whitespace-nowrap" to="/registry">
								NVCF Gateway
							</Link>
						</Anchor>
					</>
				}
			>
				<HorizontalNav
					className="hidden sm:flex"
					items={navItems}
					renderLink={(item) => (
						<Link {...item} to={item.href} viewTransition />
					)}
					value={activeValue}
				/>
			</AppBar>
			<main className="mx-auto w-full max-w-7xl px-[clamp(16px,5vw,32px)] py-6">
				<Outlet />
			</main>
			{showDevtools && <TanStackRouterDevtools />}
		</>
	);
}

/**
 * An unknown URL. An endpoint's Grafana link lands here when nothing else on
 * the ingress serves Grafana, so that case says so instead.
 */
function ShellNotFound() {
	const pathname = useLocation({ select: (location) => location.pathname });
	const { data: config, isPending } = useGetConfig();
	// Wait for the config rather than flash "Page not found" first.
	if (isPending) return null;
	return isGrafanaPath(pathname, config?.grafanaUrl) ? (
		<GrafanaUnavailable path={pathname} />
	) : (
		<NotFound />
	);
}

export const rootRoute = createRootRouteWithContext<{
	queryClient: QueryClient;
}>()({
	component: AppShell,
	notFoundComponent: ShellNotFound,
	head: () => ({ meta: [{ title: "NVCF Gateway" }] }),
	beforeLoad: ({ context }) => {
		void context.queryClient.prefetchQuery(getGetConfigQueryOptions());
	},
});
