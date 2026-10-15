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

package com.nvidia.boot.registries.service.registry.client.ngc;

import java.net.URI;
import org.springframework.http.HttpHeaders;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.service.annotation.GetExchange;
import org.springframework.web.service.annotation.HttpExchange;

/**
 * Unauthenticated probes used to obtain a {@code WWW-Authenticate} challenge from the registry.
 *
 * <p>These return {@link ResponseEntity} so that a 401 and its headers reach the caller. They
 * must be backed by a WebClient that lets 401 through - every other call in the flow stays on a
 * client that converts 401 into an exception.
 */
interface NgcChallengeProbeStub {

    /**
     * Pings the registry base endpoint (OCI distribution spec {@code end-1}) to trigger a
     * challenge when no image reference is available.
     */
    @GetExchange
    ResponseEntity<Void> probeBaseEndpoint(URI url);

    /**
     * Probes a manifest with HEAD to trigger its challenge. A non-401 answer means the manifest
     * is accessible without credentials - the probe then doubles as the existence check.
     */
    @HttpExchange(method = "HEAD")
    ResponseEntity<Void> probeManifest(
            URI url,
            @RequestHeader(HttpHeaders.ACCEPT) String imageMediaTypes);
}
