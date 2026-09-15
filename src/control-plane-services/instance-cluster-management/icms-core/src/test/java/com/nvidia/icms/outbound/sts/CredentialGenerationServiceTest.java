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
package com.nvidia.icms.outbound.sts;

import com.nvidia.icms.integration.IntegrationTest;
import java.util.concurrent.ExecutionException;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import software.amazon.awssdk.services.sts.model.Credentials;

public class CredentialGenerationServiceTest extends IntegrationTest {

    @Autowired
    private CredentialsGenerationService credentialsGenerationService;

    @Test
    void getCredentialsForQueue_success()
            throws ExecutionException, InterruptedException {

        Credentials credentials = credentialsGenerationService.getCredentialsForQueue(
                "http://localhost:4566/000000000000/gdn-spot-instance-requests-global.fifo");

        Assertions.assertNotNull(credentials);
        Assertions.assertNotEquals("", credentials.accessKeyId());
        Assertions.assertNotEquals("", credentials.secretAccessKey());
        Assertions.assertNotEquals("", credentials.sessionToken());
        Assertions.assertNotNull(credentials.expiration());
    }

    @Test
    void getCredentialsForQueue_invalidArn_exception()
            throws ExecutionException, InterruptedException {

        IllegalArgumentException e = Assertions.assertThrows(IllegalArgumentException.class,
                                                             () -> credentialsGenerationService.getCredentialsForQueue(
                                                                     "dummy_url"));

        Assertions.assertEquals("cannot parse queue url dummy_url", e.getMessage());
    }
}
