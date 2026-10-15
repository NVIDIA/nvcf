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

package com.nvidia.boot.mock.oauth2;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.client.MappingBuilder;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.concurrent.locks.ReentrantLock;
import lombok.Getter;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import wiremock.org.apache.hc.core5.http.ContentType;
import wiremock.org.eclipse.jetty.http.HttpStatus;

/**
 * Mock OAuth2 Token Server for integration tests.
 */
@RequiredArgsConstructor
public class MockOAuth2TokenServer {

    @Getter
    private final OAuth2TokenServerConfigurationProperties tokenServerConfiguration;

    @SneakyThrows
    public WireMockServer start() {
        var url = URI.create(tokenServerConfiguration.keysetUrl()).toURL();
        var port = url.getPort();
        var jwkSetUriPath = url.getPath();
        return WireMockOAuth2TokenServer.start(port,
                                               WireMockOAuth2TokenServer
                                                       .getDefaultStub(jwkSetUriPath));
    }

    @SneakyThrows
    public WireMockServer start(List<MappingBuilder> stubs) {
        var url = URI.create(tokenServerConfiguration.keysetUrl()).toURL();
        var port = url.getPort();
        return WireMockOAuth2TokenServer.start(port, stubs);
    }

    public void stop() {
        WireMockOAuth2TokenServer.stop();
    }

    @SneakyThrows
    public String getJwt(String... scopes) {
        var clients = tokenServerConfiguration.clientBindings();
        if (clients == null || clients.isEmpty()) {
            clients = List.of("test-client");
        }
        return getJwt(clients.get(0), List.of(scopes), 100);
    }

    @SneakyThrows
    public String getJwt(String subject, List<String> scopes, int expireInSeconds) {
        return getJwt(subject, scopes, expireInSeconds, null);
    }

    @SneakyThrows
    public String getJwt(String subject, List<String> scopes, int expireInSeconds, String options) {
        return OAuth2TestUtils.getJwt(subject, scopes,
                                      expireInSeconds,
                                      URI.create(tokenServerConfiguration.issuer()).toURL(),
                                      options);
    }

    @SneakyThrows
    public String getJwt(
            String subject, List<String> scopes, int expireInSeconds, String options,
            Map<String, String> metadata) {
        return OAuth2TestUtils.getJwt(subject, scopes, expireInSeconds,
                                      URI.create(tokenServerConfiguration.issuer()).toURL(),
                                      options, metadata);
    }

    @SneakyThrows
    public String getServiceID() {
        return OAuth2TestUtils.getServiceId(URI.create(tokenServerConfiguration.issuer()).toURL());
    }

    public String getBaseUrl() {
        return WireMockOAuth2TokenServer.getBaseUrl();
    }

    private static class WireMockOAuth2TokenServer {

        private static final ReentrantLock lock = new ReentrantLock();

        private static WireMockServer wireMockServer;

        public static WireMockServer start(int port, List<MappingBuilder> stubs) {
            lock.lock();
            try {
                if (wireMockServer != null && wireMockServer.isRunning()) {
                    return wireMockServer;
                }

                var configuration = new WireMockConfiguration()
                        .port(port)
                        .extensions(new TokenEndpointResponseTransformer());

                wireMockServer = new WireMockServer(configuration);
                stubs.forEach(wireMockServer::stubFor);
                wireMockServer.start();
                return wireMockServer;
            } finally {
                lock.unlock();
            }
        }

        public static void stop() {
            lock.lock();
            try {
                if (wireMockServer == null || !wireMockServer.isRunning()) {
                    return;
                }

                wireMockServer.stop();
            } finally {
                lock.unlock();
            }
        }

        public static String getBaseUrl() {
            return wireMockServer.baseUrl();
        }

        public static List<MappingBuilder> getDefaultStub(String jwkSetUriPath) {
            var mimeTypeJson = ContentType.APPLICATION_JSON.getMimeType();
            var pubKeys = get(urlPathEqualTo(jwkSetUriPath))
                    .willReturn(aResponse()
                                        .withStatus(HttpStatus.OK_200)
                                        .withHeader(CONTENT_TYPE, mimeTypeJson)
                                        .withBody(OAuth2TestUtils.getJwks().toString()));
            var token = post(urlPathMatching("/token"))
                    .willReturn(aResponse()
                                        .withStatus(HttpStatus.OK_200)
                                        .withHeader(CONTENT_TYPE, mimeTypeJson)
                                        .withTransformers(TokenEndpointResponseTransformer.NAME));
            return List.of(pubKeys, token);
        }
    }
}
