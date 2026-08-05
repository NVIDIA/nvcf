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
package com.nvidia.ess.encryption.it;

import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.SpringBootConfiguration;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.data.cassandra.repository.config.EnableReactiveCassandraRepositories;

// No autoconfiguration excludes are needed here: the multipart / kafka / orm.jpa / sql.init
// autoconfigure classes are not on this reactive-cassandra test classpath, so they cannot activate
// and there is nothing to exclude.
@SpringBootApplication(scanBasePackages = "com.nvidia.ess.encryption")
@EnableReactiveCassandraRepositories(basePackages = {"com.nvidia.ess.encryption"})
@SpringBootConfiguration
@Slf4j
public class TestApplication {

}