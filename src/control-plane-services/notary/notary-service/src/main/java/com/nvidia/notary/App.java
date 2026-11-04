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
package com.nvidia.notary;

import com.nvidia.boot.observability.tracing.cassandra.CassandraTracingAutoConfiguration;
import com.nvidia.notary.config.ConfigurationValidator;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.micrometer.tracing.autoconfigure.prometheus.PrometheusExemplarsAutoConfiguration;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.context.ConfigurableApplicationContext;

// CassandraTracingAutoConfiguration is shipped by nv-boot-starter-observability 1.9.0+ and
// references com.datastax.oss:java-driver-core. Notary does not use Cassandra; excluding it
// avoids NoClassDefFoundError on RequestTracker when Spring enhances the @Configuration class.
@SpringBootApplication(
        exclude = {PrometheusExemplarsAutoConfiguration.class, CassandraTracingAutoConfiguration.class})
public class App {

    private static final Logger log = LoggerFactory.getLogger(App.class);

    public static void main(String[] args) {
        ConfigurableApplicationContext context = SpringApplication.run(App.class, args);
        var configValidator = context.getBean(ConfigurationValidator.class);
        try {
            configValidator.validate();
        } catch (IllegalStateException e) {
            // ConfigurationValidator.validate() reports invalid signing keys, scope, issuer URL,
            // and audience binding via IllegalStateException — see notary-core ConfigurationValidator.
            log.error(e.getMessage());
            SpringApplication.exit(context, () -> 1);
        }
    }
}
