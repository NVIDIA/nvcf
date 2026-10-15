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
	Button,
	type ButtonProps,
	Tooltip,
} from "@nvidia/foundations-react-core";
import { Check, Copy } from "lucide-react";
import { useRef } from "react";
import { copyToClipboard } from "~/utils/clipboard";

interface CopyButtonProps {
	/** The text to copy to the clipboard. */
	value: string;
	/** Accessible label for the button. */
	ariaLabel?: string;
	/** Tooltip shown on hover. */
	tooltip?: string;
	size?: ButtonProps["size"];
	className?: string;
	iconSize?: number;
}

/**
 * Icon button that copies `value` to the clipboard, with a brief copy→check
 * animation on success. Copies via {@link copyToClipboard}, which falls back to
 * `execCommand` in insecure (HTTP) contexts where `navigator.clipboard` is
 * unavailable.
 */
export function CopyButton({
	value,
	ariaLabel = "Copy to clipboard",
	tooltip = "Copy",
	size = "tiny",
	className,
	iconSize = 12,
}: CopyButtonProps) {
	const ref = useRef<HTMLDivElement>(null);

	return (
		<Tooltip slotContent={tooltip}>
			<Button
				aria-label={ariaLabel}
				className={className}
				kind="tertiary"
				onClick={() => {
					void copyToClipboard(value);
					const el = ref.current;
					if (!el) return;
					el.setAttribute("data-copied", "");
					setTimeout(() => el.removeAttribute("data-copied"), 1500);
				}}
				size={size}
			>
				<div
					className="group grid place-items-center *:[grid-area:1/1]"
					ref={ref}
				>
					<Copy
						className="scale-100 opacity-100 transition-[opacity,transform] duration-200 group-data-copied:scale-50 group-data-copied:opacity-0"
						size={iconSize}
					/>
					<Check
						className="scale-50 opacity-0 transition-[opacity,transform] duration-200 group-data-copied:scale-100 group-data-copied:opacity-100"
						size={iconSize}
					/>
				</div>
			</Button>
		</Tooltip>
	);
}
