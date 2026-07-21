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

package com.nvidia.boot.mock.ngc;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.boot.mock.BootTestConstants.IMAGE_MEDIA_TYPES;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_HASH;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NOT_EXIST_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_PERMISSION_DENIED_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.Getter;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;

public class MockNgcContainerRegistryServer {

    @Getter
    private static WireMockServer ngcContainerRegistryMockServer;
    private static final String PROXY_AUTH_URL = "/proxy_auth";
    private static final String VALIDATE_MANIFEST_URL =
            "/v2/" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME + "/manifests/" +
                    TEST_VALID_CONTAINER_TAG;
    private static final String VALIDATE_MANIFEST_URL_WITH_DIGEST =
            "/v2/" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME +
                    "/manifests/" + TEST_VALID_CONTAINER_HASH;
    private static final String VALIDATE_MANIFEST_PERMISSION_DENIED_URL =
            "/v2/" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME +
                    "/manifests/" + TEST_VALID_CONTAINER_PERMISSION_DENIED_TAG;
    private static final String VALIDATE_MANIFEST_NOT_EXISTS_URL =
            "/v2/" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME +
                    "/manifests/" + TEST_VALID_CONTAINER_NOT_EXIST_TAG;
    private static final String PROXY_AUTH = """
            {
                "expires_in": 600,
                "token": "mockBearerToken"
            }
            """;
    private static final String VALIDATE_MANIFEST = """
            589386975/mega-dev/mega-scheduler-service@sha256:d3f9786af0f21490f55299ac0af2f2da871f927865b042def17c63a3699d8d51
            {
                    "schemaVersion": 2,
                    "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
                    "config": {
                            "mediaType": "application/vnd.docker.container.image.v1+json",
                            "size": 4174,
                            "digest": "sha256:9683445c1318f0d623fb43ac70cb980e9c63d3ad2b4010d898da605b0871c0da"
                    },
                    "layers": [
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 84670,
                                    "digest": "sha256:51c1b6699f435b7ccff149db8fdfc0479d802406fea5712271fac54f97eb3b8f"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 12579,
                                    "digest": "sha256:2e4cf50eeb92ac3a7afe75e15d96a26dee99449f86b46c75b5d95f4418a5bca0"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 455583,
                                    "digest": "sha256:97710db6c874ac8fba16cebc4e65a51350d130de4a86ab8f8dfd4101f97095e9"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 75,
                                    "digest": "sha256:0f8b424aa0b96c1c388a5fd4d90735604459256336853082afb61733438872b5"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 193,
                                    "digest": "sha256:d557676654e572af3e3173c90e7874644207fda32cd87e9d3d66b5d7b98a7b21"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 107,
                                    "digest": "sha256:9053c7a2530447e88fee5d33d1c5dad5aa48c039bed546c4b967d2c4247e9f77"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 173,
                                    "digest": "sha256:d858cbc252ade14879807ff8dbc3043a26bbdb92087da98cda831ee040b172b3"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 97,
                                    "digest": "sha256:1069fc2daed1aceff7232f4b8ab21200dd3d8b04f61be9da86977a34a105dfdc"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 382,
                                    "digest": "sha256:b40161cd83fc5d470d6abe50e87aa288481b6b89137012881d74187cfbf9f502"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 326,
                                    "digest": "sha256:3f4e2c5863480125882d92060440a5250766bce764fee10acdbac18c872e4dc7"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 129107,
                                    "digest": "sha256:80a8c047508ae5cd6a591060fc43422cb8e3aea1bd908d913e8f0146e2297fea"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 639820,
                                    "digest": "sha256:586cbe57ad0361ab6f127c4292c92a53158216474e586b8250ca9d7ff976bfcc"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 454347,
                                    "digest": "sha256:17f0cbbecfbe215a53d878de286df9cc51268924c69ccf8af02598ac744cdd5c"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 5088161,
                                    "digest": "sha256:e95e5abe292be5983f45cae778fa6bdbd0fdfbd21fcc8d0ff7e7057b873dd741"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 2569435,
                                    "digest": "sha256:04155d74e8b44d7e5ba2e641655c607e9e1ff0250dd650f29bc927784444a040"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 138135,
                                    "digest": "sha256:e4779a15f3b57f6794e289145a3c5524f58a5a77b23138b444de0382881d372f"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 804137,
                                    "digest": "sha256:d62a48892d6a7ca2180bcf4eab1391fc1a39fdbac3934319676e579858857c50"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 59255,
                                    "digest": "sha256:86a573e52aae0a885fc9012172c84b3451a91cc2cf77003982879284ee69d30a"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 157,
                                    "digest": "sha256:12558547ba017ef70b593a8ace770d3f38026a80c4db7547fbcb6567628b9c51"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 108,
                                    "digest": "sha256:427f7d18e1e6344a58904ff17aa981ea624a8052449ef9e7e7aa9678946f2050"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 143,
                                    "digest": "sha256:7ad1a2b3b310bbd1523dcfa1616a6bb3738ab620d1451063a9fe723e3a461706"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 5763508,
                                    "digest": "sha256:f9c166c3d1a469cb72cfd248e403f4f63530c9ead08a978d33ce4b520b6afe6b"
                            },
                            {
                                    "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                                    "size": 742,
                                    "digest": "sha256:03c530502c52456045b035612e8309481433355b4d3e52ad935e704b4cd9adab"
                            }
                    ]
            }
            """;

    @SneakyThrows
    public static void start(String ngcRegistryBaseUrl) {
        stop();
        ngcContainerRegistryMockServer = new WireMockServer(URI.create(ngcRegistryBaseUrl).getPort());
        ngcContainerRegistryMockServer.start();

        ngcContainerRegistryMockServer.stubFor(get(urlPathEqualTo(PROXY_AUTH_URL))
                                                       .willReturn(aResponse().withStatus(200)
                                                                           .withHeader(
                                                                                   HttpHeaders.CONTENT_TYPE,
                                                                                   MediaType.APPLICATION_JSON_VALUE)
                                                                           .withBody(PROXY_AUTH)));
        ngcContainerRegistryMockServer.stubFor(get(urlPathEqualTo(VALIDATE_MANIFEST_URL))
                                                       .withHeader(HttpHeaders.ACCEPT,
                                                                   equalTo(IMAGE_MEDIA_TYPES))
                                                       .willReturn(aResponse().withStatus(200)
                                                                           .withHeader(
                                                                                   HttpHeaders.CONTENT_TYPE,
                                                                                   MediaType.APPLICATION_JSON_VALUE)
                                                                           .withBody(
                                                                                   VALIDATE_MANIFEST)));
        ngcContainerRegistryMockServer.stubFor(
                get(urlPathEqualTo(VALIDATE_MANIFEST_URL_WITH_DIGEST))
                        .withHeader(HttpHeaders.ACCEPT,
                                    equalTo(IMAGE_MEDIA_TYPES))
                        .willReturn(aResponse().withStatus(200)
                                            .withHeader(
                                                    HttpHeaders.CONTENT_TYPE,
                                                    MediaType.APPLICATION_JSON_VALUE)
                                            .withBody(
                                                    VALIDATE_MANIFEST)));
        ngcContainerRegistryMockServer.stubFor(
                get(urlPathEqualTo(VALIDATE_MANIFEST_PERMISSION_DENIED_URL))
                        .withHeader(HttpHeaders.ACCEPT, equalTo(IMAGE_MEDIA_TYPES))
                        .willReturn(aResponse().withStatus(403)));
        ngcContainerRegistryMockServer.stubFor(get(urlPathEqualTo(VALIDATE_MANIFEST_NOT_EXISTS_URL))
                                                       .withHeader(HttpHeaders.ACCEPT,
                                                                   equalTo(IMAGE_MEDIA_TYPES))
                                                       .willReturn(aResponse().withStatus(404)));
    }

    public static void setResponse(String url, byte[] body) {
        ngcContainerRegistryMockServer.stubFor(get(urlPathEqualTo(url))
                                                       .willReturn(aResponse().withStatus(200)
                                                                           .withHeader(
                                                                                   HttpHeaders.CONTENT_TYPE,
                                                                                   MediaType.APPLICATION_JSON_VALUE)
                                                                           .withBody(body)));
    }

    public static void stop() {
        if (ngcContainerRegistryMockServer != null) {
            ngcContainerRegistryMockServer.stop();
        }
    }
}
