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

import { Button, Text, TextArea } from "@nvidia/foundations-react-core";
import { SendHorizontalIcon, SquareIcon } from "lucide-react";
import { useState } from "react";

interface ChatComposerProps {
	initialValue?: string;
	/** The model can't take requests: typing is blocked and Send is off. */
	disabled: boolean;
	streaming: boolean;
	onSend: (text: string) => void;
	onStop: () => void;
}

/** The message box: Enter sends, Shift+Enter adds a line, Stop ends a reply. */
export function ChatComposer({
	initialValue = "",
	disabled,
	streaming,
	onSend,
	onStop,
}: ChatComposerProps) {
	const [value, setValue] = useState(initialValue);
	const canSend = !disabled && !streaming && value.trim() !== "";

	const submit = () => {
		if (!canSend) return;
		onSend(value);
		setValue("");
	};

	return (
		<form
			className="flex flex-col gap-2"
			onSubmit={(event) => {
				event.preventDefault();
				submit();
			}}
		>
			<TextArea
				aria-label="Message"
				disabled={disabled}
				layout="vertical"
				onKeyDown={(event) => {
					if (
						event.key === "Enter" &&
						!event.shiftKey &&
						!event.nativeEvent.isComposing
					) {
						event.preventDefault();
						submit();
					}
				}}
				onValueChange={setValue}
				placeholder="Send a message"
				resizeable="auto"
				slotEnd={
					<div className="flex w-full justify-end">
						{streaming ? (
							<Button
								className="pointer-coarse:min-h-11"
								kind="secondary"
								onClick={onStop}
								size="small"
								type="button"
							>
								{/* The universal stop mark: a filled red square. */}
								<SquareIcon
									aria-hidden
									className="text-feedback-danger"
									fill="currentColor"
								/>
								Stop
							</Button>
						) : (
							<Button
								className="pointer-coarse:min-h-11"
								color="brand"
								disabled={!canSend}
								size="small"
								type="submit"
							>
								<SendHorizontalIcon aria-hidden />
								Send
							</Button>
						)}
					</div>
				}
				value={value}
			/>
			<Text className="text-center text-placeholder" kind="label/regular/sm">
				Models can make mistakes.
			</Text>
		</form>
	);
}
