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

package com.nvidia.apikeys.config;

import static java.lang.String.format;

import com.datastax.driver.core.Cluster;
import com.datastax.driver.core.ResultSet;
import com.datastax.driver.core.Session;
import jakarta.annotation.Nonnull;
import java.io.File;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.temporal.ChronoUnit;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.jspecify.annotations.Nullable;
import org.junit.jupiter.api.extension.BeforeEachCallback;
import org.junit.jupiter.api.extension.ExtensionContext;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.restclient.RestTemplateCustomizer;
import org.springframework.boot.test.util.TestPropertyValues;
import org.springframework.context.ApplicationContextInitializer;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Profile;
import org.springframework.core.ResolvableType;
import org.springframework.data.cassandra.repository.config.EnableCassandraRepositories;
import org.springframework.http.HttpInputMessage;
import org.springframework.http.HttpOutputMessage;
import org.springframework.http.MediaType;
import org.springframework.http.converter.HttpMessageConverter;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.http.converter.HttpMessageNotWritableException;
import org.springframework.http.converter.json.JacksonJsonHttpMessageConverter;
import org.springframework.util.StreamUtils;
import org.springframework.web.client.RestTemplate;
import org.testcontainers.containers.ComposeContainer;
import org.testcontainers.containers.wait.strategy.Wait;
import org.testcontainers.containers.wait.strategy.WaitAllStrategy;
import org.testcontainers.utility.DockerImageName;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Configuration
@Profile({"integrationtest"})
@EnableCassandraRepositories(basePackages = "com.nvidia.apikeys.persistance")
public class IntegrationTestConfiguration {

    private static final Logger STATIC_LOG =
            LoggerFactory.getLogger(IntegrationTestConfiguration.class);

    public static final String KEY_SPACE = "nvcf_api_keys";

    public static final String CASSANDRA_SERVICE_NAME = "cassandra-1";
    public static final int CASSANDRA_PORT = 9042;

    private static final String DOCKER_COMPOSE_IMAGE = "docker:24.0.2";

    public static final Session CQL_SESSION;

    public static String CASSANDRA_HOST;
    public static int CASSANDRA_MAPPED_PORT;

    static {
        var cassandraWaitStrategy = new WaitAllStrategy(WaitAllStrategy.Mode.WITH_OUTER_TIMEOUT);
        cassandraWaitStrategy.withStartupTimeout(Duration.of(3, ChronoUnit.MINUTES));
        cassandraWaitStrategy.withStrategy(
                Wait.forLogMessage(".*Cassandra init scripts executed.*", 1));

        var composeContainer = createComposeContainer(
                new File("local_env/docker-compose.test.yml"))
                .withExposedService(CASSANDRA_SERVICE_NAME,
                                    CASSANDRA_PORT,
                                    cassandraWaitStrategy)
                .withBuild(false);

        composeContainer.start();

        CASSANDRA_HOST = composeContainer.getServiceHost(CASSANDRA_SERVICE_NAME, CASSANDRA_PORT);
        CASSANDRA_MAPPED_PORT = composeContainer.getServicePort(CASSANDRA_SERVICE_NAME,
                                                                CASSANDRA_PORT);
        composeContainer.getContainerByServiceName(CASSANDRA_SERVICE_NAME)
                .ifPresentOrElse(
                        container -> STATIC_LOG.info(container.getLogs()),
                        () -> {
                            throw new IllegalStateException("Missing container "
                                                                    + CASSANDRA_SERVICE_NAME);
                        });

        Cluster cluster = Cluster.builder()
                .addContactPoint(CASSANDRA_HOST)
                .withPort(CASSANDRA_MAPPED_PORT)
                .withCredentials("cassandra", "cassandra")
                .withoutJMXReporting()
                .build();
        CQL_SESSION = cluster.connect();
    }

    // This is needed for tests to run consistently locally and also in the CI pipeline.
    // Testcontainers 2.x uses the host's Docker Compose CLI for the File-only constructor. The
    // Maven image used in the CI pipeline can access the Docker daemon, but it does not include
    // the Docker Compose CLI. So, use local Docker Compose CLI when available. Otherwise, run
    // Compose using a pinned Docker image for reproducible local and CI test execution.
    private static ComposeContainer createComposeContainer(File composeFile) {
        if (isLocalComposeAvailable()) {
            return new ComposeContainer(composeFile);
        }

        return new ComposeContainer(DockerImageName.parse(DOCKER_COMPOSE_IMAGE), composeFile);
    }

    private static boolean isLocalComposeAvailable() {
        try {
            var process = new ProcessBuilder("docker", "compose", "version")
                    .redirectErrorStream(true)
                    .start();
            boolean completed = process.waitFor(5, TimeUnit.SECONDS);
            if (!completed) {
                process.destroyForcibly();
                return false;
            }
            return process.exitValue() == 0;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        } catch (IOException e) {
            return false;
        }
    }

    /**
     * Integration tests assert exact JSON text via {@code exchange(..., String.class)}; read JSON
     * objects as raw UTF-8 on the client only (server request bodies still deserialize to DTOs).
     */
    @Bean
    RestTemplateCustomizer integrationTestRestTemplateCustomizer(JsonMapper jsonMapper) {
        return (RestTemplate restTemplate) -> {
            restTemplate.getMessageConverters().removeIf(JacksonJsonHttpMessageConverter.class::isInstance);
            restTemplate.getMessageConverters().addFirst(jsonStringAwareJacksonConverter(jsonMapper));
        };
    }

    private static HttpMessageConverter<?> jsonStringAwareJacksonConverter(JsonMapper jsonMapper) {
        return new JacksonJsonHttpMessageConverter(jsonMapper) {
            @Override
            public Object read(
                    ResolvableType type,
                    HttpInputMessage inputMessage,
                    @Nullable Map<String, Object> hints)
                    throws IOException, HttpMessageNotReadableException {
                if (type.resolve() == String.class) {
                    return StreamUtils.copyToString(inputMessage.getBody(), StandardCharsets.UTF_8);
                }
                return super.read(type, inputMessage, hints);
            }

            @Override
            protected void writeInternal(
                    Object object,
                    ResolvableType type,
                    HttpOutputMessage outputMessage,
                    @Nullable Map<String, Object> hints)
                    throws IOException, HttpMessageNotWritableException {
                if (object instanceof String json) {
                    outputMessage.getHeaders().setContentType(MediaType.APPLICATION_JSON);
                    outputMessage.getBody().write(json.getBytes(StandardCharsets.UTF_8));
                    return;
                }
                super.writeInternal(object, type, outputMessage, hints);
            }
        };
    }

    public static class Initializer implements
            ApplicationContextInitializer<ConfigurableApplicationContext> {

        @Override
        public void initialize(@Nonnull ConfigurableApplicationContext applicationContext) {
            TestPropertyValues.of(
                    "spring.cassandra.contact-points=" + CASSANDRA_HOST,
                    "spring.cassandra.port=" + CASSANDRA_MAPPED_PORT,
                    "spring.cassandra.local-datacenter=datacenter1",
                    "spring.cassandra.keyspace-name=" + KEY_SPACE,
                    "spring.cassandra.ssl.enabled=false"
            ).applyTo(applicationContext);
        }
    }

    public static class TestCleanerExtension implements BeforeEachCallback {

        private static final String GET_ALL_TABLE_NAMES_QUERY =
                "SELECT table_name FROM system_schema.tables WHERE keyspace_name = '"
                        + KEY_SPACE + "'";

        private static final String TRUNCATE_TABLE_QUERY_TEMPLATE =
                "truncate table " + KEY_SPACE + ".%s";

        @Override
        public void beforeEach(ExtensionContext context) {
            cleanDatabase();
        }

        private void cleanDatabase() {
            ResultSet resultSet = CQL_SESSION.execute(GET_ALL_TABLE_NAMES_QUERY);
            resultSet.forEach(row -> CQL_SESSION
                    .execute(format(TRUNCATE_TABLE_QUERY_TEMPLATE, row.getString("table_name"))));
        }
    }
}
