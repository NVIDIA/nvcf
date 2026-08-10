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
package com.nvidia.ess.encryption.testing;

import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import org.jetbrains.annotations.NotNull;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.test.context.TestContext;
import org.springframework.test.context.TestExecutionListener;

public class ContainerTestExecutionListener implements TestExecutionListener {

    @Autowired
    private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

    @Autowired
    private EncryptionKeyRepository encryptionKeyRepository;

    @Autowired
    private EncryptionKeyV2Repository encryptionKeyV2Repository;

    private volatile boolean autowiredAlready = false;

    @Override
    public void beforeTestClass(@NotNull TestContext testContext) {
        CustomCassandraContainer.container.start();
    }

    @Override
    public void afterTestClass(@NotNull TestContext testContext) {

        if (!autowiredAlready) {
            synchronized (this) {
                if (!autowiredAlready) {
                    testContext.getApplicationContext()
                            .getAutowireCapableBeanFactory()
                            .autowireBean(this);
                    autowiredAlready = true;
                }
            }
        }

        encryptionKeyRepository.deleteAll().block();
        encryptionKeyByTimestampRepository.deleteAll().block();
        encryptionKeyV2Repository.deleteAll().block();
    }
}
