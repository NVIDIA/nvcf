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
package com.nvidia.nvcf.rest.function.invocation.dto;

import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;

/**
 * <a href="https://www.oauth.com/oauth2-servers/access-tokens/access-token-response/">oauth2 access token response format</a>
 */
@Schema(description = "Function invocation token response")
public record InvocationTokenResponse(
        @Schema(description = "Token issued by Notary Service")
        @JsonProperty("access_token")
        String accessToken,
        @Schema(description = "Token expiration time in seconds")
        @JsonProperty("expires_in")
        Integer expiresIn
) {

}
