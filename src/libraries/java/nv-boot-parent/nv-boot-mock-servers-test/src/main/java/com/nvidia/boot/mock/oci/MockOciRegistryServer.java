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

package com.nvidia.boot.mock.oci;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.head;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_HELM_CHART_MANIFEST_NOT_EXISTS_URL;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_HELM_CHART_MANIFEST_PERMISSION_DENIED_URL;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_HELM_CHART_MANIFEST_URL_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_HELM_CHART_MANIFEST_URL_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_IMAGE_MANIFEST_LATEST_URL;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_IMAGE_MANIFEST_NOT_EXISTS_URL;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_IMAGE_MANIFEST_PERMISSION_DENIED_URL;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_IMAGE_MANIFEST_URL_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_OCI_IMAGE_MANIFEST_URL_WITH_TAG;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;

public class MockOciRegistryServer {

    private WireMockServer ociRegistryMockServer;

    @SneakyThrows
    public void start(String ociRegistryBaseUrl) {
        stop();
        ociRegistryMockServer = new WireMockServer(URI.create(ociRegistryBaseUrl).getPort());
        ociRegistryMockServer.start();

        // Valid container image tag - 200
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_IMAGE_MANIFEST_URL_WITH_TAG))
                                 .willReturn(aResponse().withStatus(200)));

        // Valid container image tag: latest - 200
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_IMAGE_MANIFEST_LATEST_URL))
                                 .willReturn(aResponse().withStatus(200)));

        // Valid container image digest - 200
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_IMAGE_MANIFEST_URL_WITH_DIGEST))
                                 .willReturn(aResponse().withStatus(200)));

        // Container image permission denied
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_IMAGE_MANIFEST_PERMISSION_DENIED_URL))
                                 .willReturn(aResponse().withStatus(403)));

        // Container image does not exist
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_IMAGE_MANIFEST_NOT_EXISTS_URL))
                                 .willReturn(aResponse().withStatus(404)));

        // Valid helm chart tag - 200
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_HELM_CHART_MANIFEST_URL_WITH_TAG))
                                 .willReturn(aResponse().withStatus(200)));

        // Valid helm chart digest - 200
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_HELM_CHART_MANIFEST_URL_WITH_DIGEST))
                                 .willReturn(aResponse().withStatus(200)));

        // Helm chart permission denied
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_HELM_CHART_MANIFEST_PERMISSION_DENIED_URL))
                                 .willReturn(aResponse().withStatus(403)));

        // Helm chart does not exist
        ociRegistryMockServer
                .stubFor(head(urlMatching(TEST_OCI_HELM_CHART_MANIFEST_NOT_EXISTS_URL))
                                 .willReturn(aResponse().withStatus(404)));
    }

    public void stop() {
        if (ociRegistryMockServer != null) {
            ociRegistryMockServer.stop();
        }
    }
}
