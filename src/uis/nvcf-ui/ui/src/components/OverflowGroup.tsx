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

import { Button, Popover } from "@nvidia/foundations-react-core";
import {
	Children,
	type ReactNode,
	useCallback,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { observe, unobserve } from "~/utils/resize-observer";

interface OverflowGroupProps {
	children: ReactNode;
	className?: string;
	kind?: "expand" | "popover";
	lines?: number;
	gap?: number;
}

export function OverflowGroup({
	children,
	className,
	kind = "expand",
	lines = 1,
	gap = 8,
}: OverflowGroupProps) {
	const containerRef = useRef<HTMLDivElement>(null);
	const measureRef = useRef<HTMLDivElement>(null);
	const [visibleCount, setVisibleCount] = useState<number | null>(null);
	const [expanded, setExpanded] = useState(false);
	const [buttonRef, setButtonRef] = useState<HTMLButtonElement | null>(null);

	const childrenArray = useMemo(() => Children.toArray(children), [children]);

	const visibleChildren = useMemo(
		() =>
			expanded
				? childrenArray
				: visibleCount === null
					? []
					: childrenArray.slice(0, visibleCount),
		[childrenArray, expanded, visibleCount],
	);

	const hiddenChildren = useMemo(
		() => (visibleCount === null ? [] : childrenArray.slice(visibleCount)),
		[childrenArray, visibleCount],
	);

	const calculate = useCallback(() => {
		if (!containerRef.current || !measureRef.current || expanded) return;

		const containerWidth = containerRef.current.offsetWidth;
		const items = Array.from(measureRef.current.children) as HTMLElement[];
		const buttonWidth = buttonRef?.offsetWidth ?? 0;

		let currentLine = 1;
		let currentLineWidth = 0;
		let count = 0;

		for (let i = 0; i < items.length; i++) {
			const childWidth = items[i].offsetWidth + gap;
			const needsButton = count < items.length - 1;
			const reservedWidth = needsButton ? buttonWidth : 0;

			if (
				currentLineWidth + childWidth + reservedWidth > containerWidth &&
				currentLineWidth > 0
			) {
				currentLine++;
				currentLineWidth = childWidth;
			} else {
				currentLineWidth += childWidth;
			}

			if (currentLine > lines) break;
			count++;
		}

		setVisibleCount(count);
	}, [expanded, gap, lines, buttonRef]);

	useLayoutEffect(() => {
		const container = containerRef.current;
		if (!container) return;

		calculate();
		observe(container, calculate);
		return () => unobserve(container);
	}, [calculate]);

	const overflowButton = hiddenChildren.length > 0 && (
		<Button
			kind="tertiary"
			onClick={kind === "expand" ? () => setExpanded(true) : undefined}
			ref={setButtonRef}
			size="small"
		>
			+{hiddenChildren.length}
		</Button>
	);

	return (
		<div className={className} data-overflow={hiddenChildren.length > 0}>
			<div
				className="flex items-center overflow-hidden *:shrink-0"
				ref={containerRef}
				style={{
					gap: `${gap}px`,
					flexWrap: lines > 1 || expanded ? "wrap" : undefined,
					visibility: visibleCount !== null || expanded ? "visible" : "hidden",
				}}
			>
				{visibleChildren}
				{kind === "popover" && overflowButton ? (
					<Popover
						slotContent={
							<div className="flex flex-wrap" style={{ gap: `${gap}px` }}>
								{hiddenChildren}
							</div>
						}
					>
						{overflowButton}
					</Popover>
				) : (
					overflowButton
				)}
				{expanded && (
					<Button
						kind="tertiary"
						onClick={() => setExpanded(false)}
						size="small"
					>
						See Less
					</Button>
				)}
			</div>

			{/* Hidden measurement container */}
			<div
				aria-hidden="true"
				className="flex items-center"
				inert
				ref={measureRef}
				style={{
					gap: `${gap}px`,
					flexWrap: lines > 1 ? "wrap" : undefined,
					pointerEvents: "none",
					visibility: "hidden",
					position: "absolute",
					top: "-9999px",
					left: "-9999px",
					height: 0,
					opacity: 0,
				}}
			>
				{childrenArray}
			</div>
		</div>
	);
}
