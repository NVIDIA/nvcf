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
package com.nvidia.nvcf;

import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServerInstanced;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import com.nvidia.nvcf.util.MockEssServer;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class LocalMockEssServer {
    private static final String ESS_BASE_URL = "http://localhost:9098";
    private static final String ESS_AUTH_URL = "http://localhost:9092";

    @SneakyThrows
    public static void main(String[] args) {
        var oauth2MockServer = getOAuth2MockServer();

        try {
            log.info("Local Mock OAuth2 Token Server starting...");
            oauth2MockServer.start();
            log.info("Local Mock ESS Server starting...");
            MockEssServer.start(ESS_BASE_URL);
            log.info("Local Mock ESS and OAuth2 Server started. Keep alive for 24 hours...");
            Thread.sleep(24 * 60 * 60 * 1000);
        } finally {
            log.info("Local Mock ESS Server stopping...");
            MockEssServer.stop();
            log.info("Local Mock ESS Server stopped");
            log.info("Local Mock OAuth2 server stopping...");
            oauth2MockServer.stop();
            log.info("Local Mock OAuth2 server stopped.");
        }
    }

    private static @NotNull MockOAuth2TokenServerInstanced getOAuth2MockServer() {
        OAuth2TokenServerConfigurationProperties oauth2ServerConfigProperties =
                new OAuth2TokenServerConfigurationProperties(
                        ESS_AUTH_URL,                                // issuer
                        ESS_AUTH_URL + "/.well-known/jwks.json",     // keysetUrl
                        "ES256",                                     // jwsAlgorithm
                        null,                                        // serviceBindings
                        List.of("dummy-icms-client-id"),             // clientBindings
                        null                                         // customBindings
                );
        return new MockOAuth2TokenServerInstanced(oauth2ServerConfigProperties);
    }
}
