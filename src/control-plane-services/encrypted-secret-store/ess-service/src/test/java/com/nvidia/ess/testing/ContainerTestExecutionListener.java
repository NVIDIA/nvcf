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
package com.nvidia.ess.testing;

import com.nvidia.ess.persistence.repositories.EntityRepository;
import com.nvidia.ess.persistence.repositories.NamespaceRepository;
import com.nvidia.ess.persistence.repositories.SecretPathRepository;
import com.nvidia.ess.persistence.repositories.SecretVersionRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.test.context.TestContext;
import org.springframework.test.context.TestExecutionListener;

/**
 * Reusable test containers
 */
public class ContainerTestExecutionListener implements TestExecutionListener {

  @Autowired
  private NamespaceRepository namespaceRepository;

  @Autowired
  private EntityRepository entityRepository;

  @Autowired
  private SecretPathRepository secretPathRepository;

  @Autowired
  private SecretVersionRepository secretVersionRepository;

  @Autowired
  private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

  @Autowired
  private EncryptionKeyRepository encryptionKeyRepository;

  private volatile boolean autowiredAlready = false;

  @Override
  public void beforeTestClass(TestContext testContext) {

    if (testContext.getTestClass().isAnnotationPresent(CassandraContainerTest.class)) {
      CustomCassandraContainer.container.start();
    }
  }

  @Override
  public void afterTestClass(TestContext testContext) {
    if (testContext.getTestClass().isAnnotationPresent(CassandraContainerTest.class)) {

      // Clean up all data in the C* test-container in between tests.
      if (!autowiredAlready) {
        synchronized(this) {
          if (!autowiredAlready) {
            testContext.getApplicationContext()
                      .getAutowireCapableBeanFactory()
                      .autowireBean(this);
            autowiredAlready = true;
          }
        }
      }

      namespaceRepository.deleteAll().block();
      encryptionKeyRepository.deleteAll().block();
      encryptionKeyByTimestampRepository.deleteAll().block();
      entityRepository.deleteAll().block();
      secretPathRepository.deleteAll().block();
      secretVersionRepository.deleteAll().block();
    }
  }
}
