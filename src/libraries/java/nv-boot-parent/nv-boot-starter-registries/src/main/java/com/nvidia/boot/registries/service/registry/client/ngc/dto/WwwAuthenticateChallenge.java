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

package com.nvidia.boot.registries.service.registry.client.ngc.dto;

/**
 * A {@code Bearer} challenge parsed from a 401 response's {@code WWW-Authenticate} headers,
 * as defined by RFC 7235 and the Docker registry token authentication specification.
 *
 * <p>{@code realm} is the token endpoint the registry advertises. It is an absolute URL that
 * may point at a different origin than the registry, and it must never be cached or assumed:
 * re-read it from every challenge so a registry-side realm move is followed automatically.
 *
 * <p>{@code service} and {@code scope} are optional and are {@code null} when the challenge
 * omits them. An empty value (for example {@code scope=""}, which NGC returns on {@code /v2/})
 * is normalized to {@code null} so callers replay only the parameters the registry actually
 * advertised.
 */
public record WwwAuthenticateChallenge(String realm, String service, String scope) {

}
