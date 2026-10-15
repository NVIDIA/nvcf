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

package com.nvidia.boot.mock.docker;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_DOCKER_TAG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_PERMISSION_DENINED_DOCKER_NAMESPACE_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_DOCKER_NAMESPACE_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_DOCKER_REPO_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_DOCKER_TAG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_NAME;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;

public class MockDockerRegistryServer {

    private static final String VALIDATE_MANIFEST_URL = "/v2/" +
            TEST_VALID_DOCKER_NAMESPACE_NAME +
            "/" + TEST_VALID_DOCKER_REPO_NAME +
            "/manifests/" + TEST_VALID_DOCKER_TAG_NAME;
    private static final String VALIDATE_HELM_MANIFEST_URL = "/v2/" +
            TEST_VALID_DOCKER_NAMESPACE_NAME +
            "/" + TEST_VALID_DOCKER_REPO_NAME +
            "/manifests/" + TEST_VALID_HELM_CHART_NAME;
    private static final String VALIDATE_MANIFEST_LATEST_URL = "/v2/" +
            TEST_VALID_DOCKER_NAMESPACE_NAME +
            "/" + TEST_VALID_DOCKER_REPO_NAME +
            "/manifests/latest";
    private static final String VALIDATE_MANIFEST_PERMISSION_DENIED_URL = "/v2/" +
            TEST_PERMISSION_DENINED_DOCKER_NAMESPACE_NAME +
            "/" + TEST_VALID_DOCKER_REPO_NAME +
            "/manifests/" + TEST_VALID_DOCKER_TAG_NAME;
    private static final String VALIDATE_MANIFEST_NOT_EXISTS_URL = "/v2/" +
            TEST_VALID_DOCKER_NAMESPACE_NAME +
            "/" + TEST_VALID_DOCKER_REPO_NAME +
            "/manifests/" + TEST_NOT_EXIST_DOCKER_TAG_NAME;
    private static final String VALID_BODY_RESPONSE = """
            {
             "schemaVersion": 2,
                "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
                "config": {
                    "mediaType": "application/vnd.docker.container.image.v1+json",
                    "size": 8705,
                    "digest": "sha256:3c05cf093d57c38edafd81260a770cfe631ed7713685a3bb1b6611b2625ed666"
                },
                "layers": [
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 49590984,
                        "digest": "sha256:6ee0baa58a3d368515336c1b5c1cade29c975e1b49a832f19e22f4c46f4a23a7"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 23582876,
                        "digest": "sha256:992a857ef57584af4efb4c62d68456f1e8513c95d6248fd796a9ea7f45da4d79"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 63990914,
                        "digest": "sha256:3861a6536e4e911503e7d2fc8f93228491ba45d1e5def0d2f3723e32e03d7466"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 202538246,
                        "digest": "sha256:e5e6faea05ead1ac9cd3244827816e2385b0d62299f7937a4574fc5a9651624c"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 6469487,
                        "digest": "sha256:91c9495e7b5aa8d2de58884c4284dc52584e0ce5041c2e394eb95f327117228b"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 16836641,
                        "digest": "sha256:663b9013dbfe8e48d1036303a9c6ca888c5f723bfa3a62ad45ceeb025f426be9"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 244,
                        "digest": "sha256:d81e31035534fba2c913fd55a22e6f44d79e2e12c1a4032ddea6cbe28ed44c91"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 3081424,
                        "digest": "sha256:2af84f8c5df41b914998374049ccf8f3957e9de129a562a49b04f5a2518405c6"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 93,
                        "digest": "sha256:a9379a1e3013f066f9033a0df990a1bc1b0dbe82c0e63ca234ba14afdbe975eb"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 172,
                        "digest": "sha256:925046b36a5f25a66cf20d11573bbba2a5a0b1811d7ad54c2d44bf9f4287ffc5"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 8752522,
                        "digest": "sha256:3c233ddc9ac45d07f0b6d0f6e2136c0e573c2a38cb7b072cef5bbab301d628f3"
                    },
                    {
                        "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                        "size": 710,
                        "digest": "sha256:7e2ab0f683d06f5cff4a3296cb3d78315208c424a6cecdabc682029059579998"
                    }
                ]
            }
            """;
    private static WireMockServer dockerRegistryMockServer;

    @SneakyThrows
    public static void start(String ngcRegistryBaseUrl) {
        stop();
        dockerRegistryMockServer = new WireMockServer(URI.create(ngcRegistryBaseUrl).getPort());
        dockerRegistryMockServer.start();

        // valid tag
        dockerRegistryMockServer
                .stubFor(get(urlMatching(VALIDATE_MANIFEST_URL))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(VALID_BODY_RESPONSE)));
        // valid tag: latest
        dockerRegistryMockServer
                .stubFor(get(urlMatching(VALIDATE_MANIFEST_LATEST_URL))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(VALID_BODY_RESPONSE)));

        //valid digest
        dockerRegistryMockServer
                .stubFor(get(urlMatching("/v2/" + TEST_VALID_DOCKER_NAMESPACE_NAME +
                                                 "/" + TEST_VALID_DOCKER_REPO_NAME +
                                                 "/manifests/sha256%3A.*")) // url encoding
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(VALID_BODY_RESPONSE)));

        // no permission
        dockerRegistryMockServer
                .stubFor(get(urlMatching(VALIDATE_MANIFEST_PERMISSION_DENIED_URL))
                                 .willReturn(aResponse().withStatus(403)));

        // does not exist
        dockerRegistryMockServer
                .stubFor(get(urlMatching(VALIDATE_MANIFEST_NOT_EXISTS_URL))
                                 .willReturn(aResponse().withStatus(404)));
        // valid helm
        dockerRegistryMockServer
                .stubFor(get(urlMatching(VALIDATE_HELM_MANIFEST_URL))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(VALID_BODY_RESPONSE)));
    }

    public static void setResponse(String url, byte[] body) {
        dockerRegistryMockServer
                .stubFor(get(urlPathEqualTo(url))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(body)));
    }

    public static void stop() {
        if (dockerRegistryMockServer != null) {
            dockerRegistryMockServer.stop();
        }
    }
}
