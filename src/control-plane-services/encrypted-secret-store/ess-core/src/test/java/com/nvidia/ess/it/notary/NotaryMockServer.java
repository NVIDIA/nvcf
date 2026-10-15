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
package com.nvidia.ess.it.notary;

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
import lombok.SneakyThrows;
import wiremock.org.apache.hc.core5.http.ContentType;
import wiremock.org.eclipse.jetty.http.HttpStatus;

public class NotaryMockServer {

    private WireMockServer wireMockServer;
    private final NotaryJwtGenerator notaryJwtGenerator;

    private final List<String> defaultAudiences;

    private final String keysetUrl;
    private final String issuer;

    private final AssertionEndpointResponseTransformer assertionEndpointResponseTransformer;

    public NotaryMockServer(String keysetUrl, String issuer, List<String> defaultAudiences,
            String notarySignAuthOauth2BaseUrl) {

        this.notaryJwtGenerator = new NotaryJwtGenerator();
        this.keysetUrl = keysetUrl;
        this.issuer = issuer;
        this.defaultAudiences = defaultAudiences;
        this.assertionEndpointResponseTransformer = new AssertionEndpointResponseTransformer(notaryJwtGenerator,
                notarySignAuthOauth2BaseUrl);
    }

    @SneakyThrows
    public WireMockServer start() {
        var url = URI.create(keysetUrl).toURL();
        var port = url.getPort();
        var jwkSetUriPath = url.getPath();

        return start(port, getDefaultStub(jwkSetUriPath));
    }

    @SneakyThrows
    public WireMockServer start(List<MappingBuilder> stubs) {
        var url = URI.create(keysetUrl).toURL();
        var port = url.getPort();
        return start(port, stubs);
    }


    private synchronized WireMockServer start(int port, List<MappingBuilder> stubs) {
        if (wireMockServer != null && wireMockServer.isRunning()) {
            return wireMockServer;
        }

        var configuration = new WireMockConfiguration()
                .port(port)
                .extensions(assertionEndpointResponseTransformer);

        wireMockServer = new WireMockServer(configuration);
        stubs.forEach(wireMockServer::stubFor);
        wireMockServer.start();
        return wireMockServer;
    }


    public synchronized void stop() {
        if (wireMockServer == null || !wireMockServer.isRunning()) {
            return;
        }

        wireMockServer.stop();
    }

    @SneakyThrows
    public String getJwt(String subject, List<String> audiences, Map<String, Object> data) {
        return notaryJwtGenerator.getJwt(subject, URI.create(issuer).toURL(), audiences, data);
    }

    @SneakyThrows
    public String getJwt(String subject, Map<String, Object> data) {
        return notaryJwtGenerator.getJwt(subject, URI.create(issuer).toURL(),
                defaultAudiences, data);
    }

    public String getBaseUrl() {
        return wireMockServer.baseUrl();
    }


    private List<MappingBuilder> getDefaultStub(String jwkSetUriPath) {
        var mimeTypeJson = ContentType.APPLICATION_JSON.getMimeType();
        var pubKeys = get(urlPathEqualTo(jwkSetUriPath))
                .willReturn(aResponse()
                        .withStatus(HttpStatus.OK_200)
                        .withHeader(CONTENT_TYPE, mimeTypeJson)
                        .withBody(notaryJwtGenerator.getJwks().toString()));

        var assertion = post(urlPathMatching("/sign"))
                .willReturn(aResponse()
                        .withStatus(HttpStatus.OK_200)
                        .withHeader(CONTENT_TYPE, mimeTypeJson)
                        .withTransformers(AssertionEndpointResponseTransformer.NAME)
                );
        return List.of(pubKeys, assertion);
    }

}
