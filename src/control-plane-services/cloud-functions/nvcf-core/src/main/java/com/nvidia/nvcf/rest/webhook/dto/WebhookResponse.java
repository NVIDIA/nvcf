/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.nvcf.rest.webhook.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotBlank;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import lombok.Builder;

@Builder
@Schema(description = "Response for webhook plugin authentication")
public record WebhookResponse(@Schema(description = "User ID") @NotBlank String userId,
                              @Schema(description = "Account identifier") @NotBlank String account,
                              @Schema(description = "Permissions for the user") @Nullable WebhookPermissions permissions,
                              @Schema(description = "Error message if authentication failed") @Nullable String error,
                              @Schema(description = "Additional claims") @Nullable Map<String, Object> claims,
                              @Schema(description = "Time to live") @Nullable Duration ttl) {

    @Builder
    @Schema(description = "Permissions for webhook plugin")
    public record WebhookPermissions(
            @Schema(description = "Publish permissions") @Nullable WebhookPermission publish,
            @Schema(description = "Subscribe permissions") @Nullable WebhookPermission subscribe,
            @Schema(description = "Response permissions") @Nullable WebhookResponsePermission response) {

    }

    @Builder
    @Schema(description = "Publish or subscribe permissions for webhook plugin")
    public record WebhookPermission(
            @Schema(description = "List of allowed subjects") @Nullable List<@NotBlank String> allow,
            @Schema(description = "List of denied subjects") @Nullable List<@NotBlank String> deny) {

    }

    @Builder
    @Schema(description = "Response permissions for webhook plugin")
    public record WebhookResponsePermission(
            @Schema(description = "Maximum number of messages") @Nullable Integer maxMsgs,
            @Schema(description = "Time to live") @Nullable Duration ttl) {

    }
}