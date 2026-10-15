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

package com.nvidia.boot.cassandra;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.datastax.oss.driver.api.core.cql.SimpleStatement;
import com.nvidia.boot.cassandra.configuration.CassandraSslBundleConfiguration;
import io.micrometer.core.instrument.LongTaskTimer;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.composite.CompositeMeterRegistry;
import io.micrometer.observation.ObservationRegistry;
import java.time.Duration;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.aop.support.AopUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.EnableAutoConfiguration;
import org.springframework.boot.cassandra.autoconfigure.CqlSessionBuilderCustomizer;
import org.springframework.boot.cassandra.autoconfigure.DriverConfigLoaderBuilderCustomizer;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.ApplicationContext;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;
import org.springframework.http.MediaType;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.ResultActions;
import org.testcontainers.cassandra.CassandraContainer;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.utility.DockerImageName;

/**
 * Module-level Cassandra integration tests. {@code spring.cassandra.session-metrics} and
 * {@code spring.cassandra.node-metrics} are set so {@code CassandraMetricsConfiguration} is active
 * and driver Micrometer metrics wiring is covered together with a live Cassandra node.
 * <p>
 * {@code spring.cassandra.ssl.enabled=false} activates {@code CassandraSslBundleConfiguration}
 * (refreshing {@code cassandraSession} with {@code ObservationRequestTracker} on the builder) without
 * setting {@code spring.cassandra.ssl.bundle} or any {@code spring.ssl.bundle.pem.*} placeholder.
 */
@SpringBootTest(classes = CassandraAutoConfiguration.class, properties = {
        "management.endpoints.web.exposure.include=health",
        "management.endpoint.health.show-details=always"
})
@EnableAutoConfiguration
@AutoConfigureMockMvc
@Testcontainers
class NvBootCassandraIntegrationTest {

    private static final String KEYSPACE = "converter_test";
    private static final String TABLE = "duration_entity";

    @Container
    static final CassandraContainer CASSANDRA = new CassandraContainer("cassandra:5");

    @DynamicPropertySource
    static void cassandraProperties(DynamicPropertyRegistry registry) {
        var contactPoint = CASSANDRA.getContactPoint();
        registry.add("spring.cassandra.contact-points", () -> contactPoint.getHostString());
        registry.add("spring.cassandra.port", () -> String.valueOf(contactPoint.getPort()));
        registry.add("spring.cassandra.local-datacenter", CASSANDRA::getLocalDatacenter);
        registry.add("spring.cassandra.keyspace-name", () -> KEYSPACE);
        registry.add("spring.cassandra.ssl.enabled", () -> "false");
        registry.add("spring.cassandra.session-metrics[0]", () -> "connected-nodes");
        registry.add("spring.cassandra.node-metrics[0]", () -> "pool.in-flight");
    }

    @Autowired
    ApplicationContext applicationContext;

    @Autowired
    CqlSession cqlSession;

    @Autowired
    CassandraTemplate cassandraTemplate;

    @Autowired
    MockMvc mockMvc;

    @BeforeAll
    static void createSchema() {
        try (var session = CqlSession.builder()
                .addContactPoint(CASSANDRA.getContactPoint())
                .withLocalDatacenter(CASSANDRA.getLocalDatacenter())
                .build()) {

            session.execute("CREATE KEYSPACE IF NOT EXISTS " + KEYSPACE
                    + " WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}");
            session.execute("USE " + KEYSPACE);
            session.execute("CREATE TABLE IF NOT EXISTS " + TABLE
                    + " (id text PRIMARY KEY, timeout duration)");
        }
    }

    @Test
    void cqlSessionConnectsAndExecutesQuery() {
        assertThat(applicationContext.getBean("cassandraSession"))
                .isInstanceOf(CassandraSslBundleConfiguration.RefreshingCqlSession.class);
        assertThat(cqlSession).isNotNull();

        ResultSet resultSet = cqlSession.execute("SELECT cluster_name FROM system.local");
        assertThat(resultSet.one()).isNotNull();
    }

    @Test
    void sslDisabledWithoutBundleCqlSessionBuilderCustomizerBeansRegistered() {
        assertThat(applicationContext.getBeanNamesForType(CqlSessionBuilderCustomizer.class))
                .isNotEmpty();
    }

    /**
     * Exercises Spring Data Cassandra observations on the refreshable {@code cassandraSession} from
     * {@link CassandraSslBundleConfiguration} ({@link org.springframework.data.cassandra.observability.ObservableCqlSessionFactory}
     * wrap + {@code ObservationRequestTracker} on the builder). This integration test uses plaintext
     * Cassandra ({@code spring.cassandra.ssl.enabled=false}).
     * After each synchronous {@code execute}, the {@link ObservationRegistry} must not retain a
     * thread-local current observation or scope, and no {@link LongTaskTimer} whose name is wired
     * from Spring Data Cassandra observations should retain active tasks.
     */
    @Test
    void refreshingSessionObservationsCompleteWithoutLongTaskTimerLeak() {
        assertThat(applicationContext.getBean("cassandraSession"))
                .isInstanceOf(CassandraSslBundleConfiguration.RefreshingCqlSession.class);
        var refreshing = applicationContext.getBean(
                "cassandraSession", CassandraSslBundleConfiguration.RefreshingCqlSession.class);
        assertThat(AopUtils.isAopProxy(refreshing.getDelegate()))
                .as("Delegate should be ObservableCqlSessionFactory AOP proxy when ObservationRegistry is present")
                .isTrue();

        var observationRegistry = applicationContext.getBean(ObservationRegistry.class);
        assertThat(observationRegistry.isNoop())
                .as("Integration uses a real ObservationRegistry from actuator auto-configuration")
                .isFalse();

        var meterRegistry = applicationContext.getBean(MeterRegistry.class);

        for (int i = 0; i < 25; i++) {
            cqlSession.execute(
                    SimpleStatement.newInstance("SELECT release_version FROM system.local"));
            assertThat(observationRegistry.getCurrentObservation())
                    .as("Observation scope should not leak on the test thread after execute %s", i)
                    .isNull();
            assertThat(observationRegistry.getCurrentObservationScope())
                    .as("Observation scope handle should be cleared after execute %s", i)
                    .isNull();
            assertThat(activeLongTaskTimerTasksForObservationNames(meterRegistry))
                    .as("No observation-backed LongTaskTimer leak after execute %s", i)
                    .isZero();
        }
    }

    /**
     * {@link CompositeMeterRegistry#find(String)} does not always delegate to child registries the
     * way {@link CompositeMeterRegistry#getMeters()} does. Timers for observations often live on
     * a child.
     */
    private static java.util.List<MeterRegistry> meterRegistriesToSearch(MeterRegistry root) {
        if (root instanceof CompositeMeterRegistry composite) {
            return composite.getRegistries().stream().toList();
        }
        return java.util.List.of(root);
    }

    /**
     * Micrometer observation long tasks are named after the observation
     * (e.g. {@code spring.data.cassandra.query}). Other {@link LongTaskTimer} meters (executors,
     * unrelated components) may be non-idle during startup.
     */
    private static long activeLongTaskTimerTasksForObservationNames(MeterRegistry meterRegistry) {
        long active = 0;
        for (var reg : meterRegistriesToSearch(meterRegistry)) {
            for (var meter : reg.getMeters()) {
                if (meter instanceof LongTaskTimer longTaskTimer) {
                    var name = longTaskTimer.getId().getName();
                    if (name.startsWith("spring.data.cassandra")
                            || name.startsWith("spring.data.")) {
                        active += longTaskTimer.activeTasks();
                    }
                }
            }
        }
        return active;
    }

    @Test
    void cassandraMetricsConfigurationRegistersDriverCustomizer() {
        assertThat(applicationContext.getBeanNamesForType(DriverConfigLoaderBuilderCustomizer.class))
                .as("CassandraMetricsConfiguration should register configLoaderBuilderCustomizer when metrics lists are set")
                .contains("configLoaderBuilderCustomizer");
    }

    @Test
    void durationRoundTripViaCassandraTemplate() {
        var id = "test-" + System.currentTimeMillis();
        var timeout = Duration.ofSeconds(30).plusNanos(500_000_000);
        var entity = new DurationEntity(id, timeout);

        cassandraTemplate.insert(entity);

        var loaded = cassandraTemplate.selectOneById(id, DurationEntity.class);
        assertThat(loaded).isNotNull();
        assertThat(loaded.id).isEqualTo(id);
        assertThat(loaded.timeout).isEqualTo(timeout);
    }

    @Test
    void durationWithDaysRoundTripViaCassandraTemplate() {
        var id = "test-days-" + System.currentTimeMillis();
        var timeout = Duration.ofDays(1).plusHours(2).plusMinutes(30);
        var entity = new DurationEntity(id, timeout);

        cassandraTemplate.insert(entity);

        var loaded = cassandraTemplate.selectOneById(id, DurationEntity.class);
        assertThat(loaded).isNotNull();
        assertThat(loaded.timeout).isEqualTo(timeout);
    }

    @Test
    void actuatorHealthIncludesCassandraWithUpStatus() throws Exception {
        ResultActions result = mockMvc.perform(get("/actuator/health")
                .accept(MediaType.APPLICATION_JSON));

        result.andExpect(status().isOk());

        var json = result.andReturn().getResponse().getContentAsString();
        assertThat(json).contains("\"cassandra\"");
        assertThat(json).contains("\"status\":\"UP\"");
    }

    @Test
    void overallHealthIsUpWhenCassandraConnected() throws Exception {
        mockMvc.perform(get("/actuator/health").accept(MediaType.APPLICATION_JSON))
                .andExpect(status().isOk())
                .andExpect(r -> {
                    var json = r.getResponse().getContentAsString();
                    assertThat(json).contains("\"status\":\"UP\"");
                });
    }

    @Table(TABLE)
    public static class DurationEntity {

        @PrimaryKey
        private String id;
        private Duration timeout;

        public DurationEntity() {
        }

        public DurationEntity(String id, Duration timeout) {
            this.id = id;
            this.timeout = timeout;
        }

        public String getId() {
            return id;
        }

        public void setId(String id) {
            this.id = id;
        }

        public Duration getTimeout() {
            return timeout;
        }

        public void setTimeout(Duration timeout) {
            this.timeout = timeout;
        }
    }
}
