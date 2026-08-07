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

import java.time.Duration;
import lombok.extern.slf4j.Slf4j;
import org.springframework.core.io.ClassPathResource;
import org.testcontainers.cassandra.CassandraContainer;
import org.testcontainers.lifecycle.Startable;
import org.testcontainers.utility.DockerImageName;

@Slf4j
public class CustomCassandraContainer implements Startable {
  // Not using Junit5 @Container to avoid container cleanup after a test class ends.
  // This means that we are not calling container.stop() anywhere explicitly.
  // However, ryuk container will be responsible for full cleanup of the testcontainers after all tests are run

  private static final CassandraContainer cassandraContainer = new CassandraContainer(DockerImageName.parse("cassandra:4"))
      .withEnv("HEAP_NEWSIZE", "128M")
      .withEnv("MAX_HEAP_SIZE", "512M")
      .withInitScript(new ClassPathResource("local_env/cassandra/schema/schema.cql").getPath())
      .withStartupTimeout(Duration.ofSeconds(120));

  // singleton
  public static final CustomCassandraContainer container = new CustomCassandraContainer();

  @Override
  public void start() {
    // container.start() is noop if previously started. Safe to call multiple times
    cassandraContainer.start();

    // works as BeforeAll callback is run by Junit before spring context is brought up
    System.setProperty("spring.cassandra.contact-points", cassandraContainer.getHost());
    var mappedPort = cassandraContainer.getMappedPort(9042);
    log.info("CASSANDRA CONTAINER PORT: " + mappedPort);
    System.setProperty("spring.cassandra.port", String.valueOf(mappedPort));
  }

  @Override
  public void stop() {
    // noop, ryuk will cleanup
  }
}
