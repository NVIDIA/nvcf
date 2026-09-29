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
package com.nvidia.ess.it;

import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.ess.EssCoreTestApp;
import com.nvidia.ess.controller.request.SecretQueryType;
import com.nvidia.ess.testing.CassandraContainerTest;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.function.IntConsumer;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.test.context.ContextConfiguration;


/**
 * Performance integration tests that verify the optimized entity type lookup
 * performs well even when a namespace has many tombstoned entity types.
 * 
 * These tests create a namespace with many entity types, tombstone all but one,
 * then perform CRUDL operations on secrets in the remaining entity type and
 * verify the operations complete in a reasonable time using percentile measurements.
 */
@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = EssCoreTestApp.class, webEnvironment = WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active:integration-test",
        })
@ContextConfiguration
@CassandraContainerTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class PerformanceIT extends IntegrationTestsBase {

    private static final String PERF_TEST_NS = UUID.randomUUID().toString();
    
    // Number of entity types to create and tombstone
    // This simulates a namespace that has accumulated many tombstoned entity types
    // from repeated test runs without garbage collection
    private static final int NUM_ENTITY_TYPES_TO_TOMBSTONE = 1000;
    
    // The one entity type that remains active
    private static final String ACTIVE_ENTITY_TYPE = "activeEntityType";
    
    // Number of concurrent client threads consuming from the work queue
    private static final int NUM_THREADS = 3;
    
    // Total number of iterations (work items in queue)
    // Need at least 1000 for meaningful p99 measurement
    private static final int TOTAL_ITERATIONS = 1000;
    
    // Performance thresholds for p99 latency (ms)
    private static final long P99_READ_THRESHOLD_MS = 500;
    // Write operations (CREATE/DELETE) are slower due to LWT overhead
    private static final long P99_WRITE_THRESHOLD_MS = 2500;
    
    private static final Map<String, Object> sampleSecretData = Map.of(
            "key1", "value1",
            "key2", "value2",
            "nested", Map.of("innerKey", "innerValue")
    );

    /**
     * Measures latencies for an operation over multiple iterations using concurrent threads
     * that consume work items from a shared queue.
     * 
     * @param operationName Name for logging
     * @param operation Operation to execute, receives a unique iteration index (0 to TOTAL_ITERATIONS-1)
     * @return Latency statistics including percentiles
     */
    private LatencyStats measureLatenciesConcurrent(String operationName, IntConsumer operation) {
        // Create work queue with all iteration indices
        BlockingQueue<Integer> workQueue = new LinkedBlockingQueue<>();
        for (int i = 0; i < TOTAL_ITERATIONS; i++) {
            workQueue.add(i);
        }
        
        List<Long> latencies = new CopyOnWriteArrayList<>();
        ExecutorService executor = Executors.newFixedThreadPool(NUM_THREADS);
        CountDownLatch startLatch = new CountDownLatch(1);
        CountDownLatch doneLatch = new CountDownLatch(NUM_THREADS);
        
        // Submit worker threads that consume from the queue
        for (int t = 0; t < NUM_THREADS; t++) {
            executor.submit(() -> {
                try {
                    // Wait for all threads to be ready
                    startLatch.await();
                    
                    // Consume work items from queue until empty
                    Integer iterationIndex;
                    while ((iterationIndex = workQueue.poll()) != null) {
                        long start = System.nanoTime();
                        operation.accept(iterationIndex);
                        long elapsed = (System.nanoTime() - start) / 1_000_000; // Convert to ms
                        latencies.add(elapsed);
                    }
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                } finally {
                    doneLatch.countDown();
                }
            });
        }
        
        // Start all threads simultaneously
        startLatch.countDown();
        
        // Wait for all threads to complete
        try {
            boolean completed = doneLatch.await(10, TimeUnit.MINUTES);
            if (!completed) {
                log.error("Performance test timed out after 10 minutes");
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } finally {
            executor.shutdown();
        }
        
        // Calculate percentiles
        List<Long> sortedLatencies = new ArrayList<>(latencies);
        Collections.sort(sortedLatencies);
        
        int size = sortedLatencies.size();
        if (size != TOTAL_ITERATIONS) {
            log.error("{}: Expected {} latencies but got {} (some operations may have failed)",
                    operationName, TOTAL_ITERATIONS, size);
        }
        long p50 = sortedLatencies.get((int) (size * 0.50));
        long p90 = sortedLatencies.get((int) (size * 0.90));
        long p99 = sortedLatencies.get((int) (size * 0.99));
        long min = sortedLatencies.get(0);
        long max = sortedLatencies.get(size - 1);
        double avg = sortedLatencies.stream().mapToLong(Long::longValue).average().orElse(0);
        
        log.warn("{} latencies (ms) over {} iterations with {} threads: min={}, avg={:.1f}, p50={}, p90={}, p99={}, max={}",
                operationName, size, NUM_THREADS, min, avg, p50, p90, p99, max);
        
        return new LatencyStats(size, min, avg, p50, p90, p99, max);
    }
    
    private record LatencyStats(int totalIterations, long min, double avg, long p50, long p90, long p99, long max) {}

    // ==============================================
    // SETUP: Create namespace with many entity types
    // ==============================================

    @Test
    @Order(1)
    void setup_createNamespace() {
        log.warn("Creating namespace for performance tests: {}", PERF_TEST_NS);
        verifySuccessResponse(createNamespace(PERF_TEST_NS), HttpStatus.OK);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(2)
    void setup_registerTenantAuthorizations() {
        log.warn("Registering tenant authorizations for performance tests");
        
        // Register NS_ADMIN authorization (using operator token)
        verifySuccessResponse(addAuthorization(PERF_TEST_NS,
                integrationTestProperties.getTenant().getNsAdmin().getIss(),
                integrationTestProperties.getTenant().getNsAdmin().getSub(),
                true, false), HttpStatus.OK);
        
        // Register SECRET_ADMIN authorization (using operator token)
        verifySuccessResponse(addAuthorization(PERF_TEST_NS,
                integrationTestProperties.getTenant().getSecretAdmin().getIss(),
                integrationTestProperties.getTenant().getSecretAdmin().getSub(),
                true, false), HttpStatus.OK);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(3)
    void setup_registerNotaryAuthorization() {
        log.warn("Registering notary authorization for performance tests");
        
        // Register NOTARY authorization (must use NS_ADMIN token, not operator token)
        verifySuccessResponse(addAuthorization(PERF_TEST_NS,
                integrationTestProperties.getTenant().getNotary().getIss(),
                integrationTestProperties.getTenant().getNotary().getSub(),
                false, true), HttpStatus.OK);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(4)
    void setup_createManyEntityTypes() {
        log.warn("Creating {} entity types to be tombstoned (this may take a while...)", NUM_ENTITY_TYPES_TO_TOMBSTONE);
        
        long startTime = System.currentTimeMillis();
        
        // Create many entity types that will be tombstoned (using NS_ADMIN token via isOperator=false)
        for (int i = 0; i < NUM_ENTITY_TYPES_TO_TOMBSTONE; i++) {
            verifySuccessResponse(createEntityType(PERF_TEST_NS, "tombstonedType" + i, false), HttpStatus.OK);
            if ((i + 1) % 100 == 0) {
                log.warn("Created {}/{} entity types...", i + 1, NUM_ENTITY_TYPES_TO_TOMBSTONE);
            }
        }
        
        long elapsed = System.currentTimeMillis() - startTime;
        log.warn("Created {} entity types in {} ms ({} ms/entity-type avg)", 
                NUM_ENTITY_TYPES_TO_TOMBSTONE, elapsed, elapsed / NUM_ENTITY_TYPES_TO_TOMBSTONE);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", NUM_ENTITY_TYPES_TO_TOMBSTONE);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(5)
    void setup_createActiveEntityType() {
        log.warn("Creating the active entity type: {}", ACTIVE_ENTITY_TYPE);
        verifySuccessResponse(createEntityType(PERF_TEST_NS, ACTIVE_ENTITY_TYPE, false), HttpStatus.OK);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(6)
    void setup_tombstoneAllButActiveEntityType() {
        log.warn("Tombstoning {} entity types (this may take a while...)", NUM_ENTITY_TYPES_TO_TOMBSTONE);
        
        long startTime = System.currentTimeMillis();
        
        // Tombstone all entity types except the active one
        for (int i = 0; i < NUM_ENTITY_TYPES_TO_TOMBSTONE; i++) {
            deleteEntityTypeSuccess(PERF_TEST_NS, "tombstonedType" + i, oauth2Tokens.get(TestTokenType.NS_ADMIN));
            if ((i + 1) % 100 == 0) {
                log.warn("Tombstoned {}/{} entity types...", i + 1, NUM_ENTITY_TYPES_TO_TOMBSTONE);
            }
        }
        
        long elapsed = System.currentTimeMillis() - startTime;
        log.warn("Tombstoned {} entity types in {} ms ({} ms/entity-type avg)", 
                NUM_ENTITY_TYPES_TO_TOMBSTONE, elapsed, elapsed / NUM_ENTITY_TYPES_TO_TOMBSTONE);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", NUM_ENTITY_TYPES_TO_TOMBSTONE);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(7)
    void setup_createBaselineSecrets() {
        log.warn("Creating {} baseline secrets for read/list tests (one per entity)", TOTAL_ITERATIONS);
        
        // Create one secret per unique entity-ID for read tests
        // This ensures each read operation targets a different entity
        for (int i = 0; i < TOTAL_ITERATIONS; i++) {
            verifySuccessResponse(createOrUpdateSecret(PERF_TEST_NS, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                    ACTIVE_ENTITY_TYPE + "/entity" + i + "/secret", sampleSecretData), HttpStatus.OK);
            if ((i + 1) % 100 == 0) {
                log.warn("Created {}/{} baseline secrets...", i + 1, TOTAL_ITERATIONS);
            }
        }
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", TOTAL_ITERATIONS);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==============================================
    // PERFORMANCE TESTS: READ operations
    // ==============================================

    @Test
    @Order(8)
    void getSecret_withManyTombstonedEntityTypes_p99Performant() {
        int totalIterations = TOTAL_ITERATIONS;
        log.warn("Testing GET secret p99 latency with {} tombstoned entity types over {} iterations ({} threads)",
                NUM_ENTITY_TYPES_TO_TOMBSTONE, totalIterations, NUM_THREADS);
        
        LatencyStats stats = measureLatenciesConcurrent("GET secret", iteration -> {
            // Each iteration reads from a different entity
            webTestClient.get()
                    .uri(buildUrl("/v1/" + ACTIVE_ENTITY_TYPE + "/entity" + iteration + "/secret"))
                    .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                    .header("X-ESS-NAMESPACE", PERF_TEST_NS)
                    .exchange()
                    .expectStatus()
                    .isOk()
                    // Drain the response body so Reactor-Netty releases the client-side
                    // ByteBuf. Without this, the undrained buffer is only reclaimed at GC
                    // and Netty's leak detector reports it as a (false-positive) LEAK.
                    .expectBody();
        });
        
        assertTrue(stats.p99() < P99_READ_THRESHOLD_MS,
                "GET secret p99 latency should be < " + P99_READ_THRESHOLD_MS + " ms, was " + stats.p99() + " ms");
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", stats.totalIterations());
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(9)
    void getSecretWithNotaryToken_withManyTombstonedEntityTypes_p99Performant() {
        int totalIterations = TOTAL_ITERATIONS;
        log.warn("Testing GET secret with notary token p99 latency with {} tombstoned entity types over {} iterations ({} threads)",
                NUM_ENTITY_TYPES_TO_TOMBSTONE, totalIterations, NUM_THREADS);
        
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        
        LatencyStats stats = measureLatenciesConcurrent("GET secret (notary)", iteration -> {
            // Each iteration reads from a different entity with a fresh notary token
            String secretPath = ACTIVE_ENTITY_TYPE + "/entity" + iteration + "/secret";
            var notaryToken = getEssAssertion(notarySub, PERF_TEST_NS, List.of(secretPath));
            
            webTestClient.get()
                    .uri(buildUrl("/v1/" + secretPath))
                    .header("X-ESS-TOKEN", notaryToken)
                    .header("X-ESS-NAMESPACE", PERF_TEST_NS)
                    .exchange()
                    .expectStatus()
                    .isOk()
                    // Drain the response body so Reactor-Netty releases the client-side
                    // ByteBuf. Without this, the undrained buffer is only reclaimed at GC
                    // and Netty's leak detector reports it as a (false-positive) LEAK.
                    .expectBody();
        });
        
        assertTrue(stats.p99() < P99_READ_THRESHOLD_MS,
                "GET secret (notary) p99 latency should be < " + P99_READ_THRESHOLD_MS + " ms, was " + stats.p99() + " ms");
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", stats.totalIterations());
    }

    // ==============================================
    // PERFORMANCE TESTS: LIST operations
    // ==============================================

    @Test
    @Order(10)
    void listSecretVersions_withManyTombstonedEntityTypes_p99Performant() {
        int totalIterations = TOTAL_ITERATIONS;
        log.warn("Testing LIST secret versions p99 latency with {} tombstoned entity types over {} iterations ({} threads)",
                NUM_ENTITY_TYPES_TO_TOMBSTONE, totalIterations, NUM_THREADS);
        
        LatencyStats stats = measureLatenciesConcurrent("LIST versions", iteration -> {
            // Each iteration lists versions from a different entity's secret
            webTestClient.get()
                    .uri(uriBuilder -> uriBuilder
                            .path("/v1/" + ACTIVE_ENTITY_TYPE + "/entity" + iteration + "/secret")
                            .queryParam("query_type", SecretQueryType.LIST_VERSIONS.name())
                            .build())
                    .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                    .header("X-ESS-NAMESPACE", PERF_TEST_NS)
                    .exchange()
                    .expectStatus()
                    .isOk()
                    // Drain the response body so Reactor-Netty releases the client-side
                    // ByteBuf. Without this, the undrained buffer is only reclaimed at GC
                    // and Netty's leak detector reports it as a (false-positive) LEAK.
                    .expectBody();
        });
        
        assertTrue(stats.p99() < P99_READ_THRESHOLD_MS,
                "LIST versions p99 latency should be < " + P99_READ_THRESHOLD_MS + " ms, was " + stats.p99() + " ms");
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", stats.totalIterations());
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(11)
    void listSecretPaths_withManyTombstonedEntityTypes_p99Performant() {
        int totalIterations = TOTAL_ITERATIONS;
        log.warn("Testing LIST secret paths p99 latency with {} tombstoned entity types over {} iterations ({} threads)",
                NUM_ENTITY_TYPES_TO_TOMBSTONE, totalIterations, NUM_THREADS);
        
        LatencyStats stats = measureLatenciesConcurrent("LIST paths", iteration -> {
            // Each iteration lists paths from a different entity
            webTestClient.get()
                    .uri(uriBuilder -> uriBuilder
                            .path("/v1/" + ACTIVE_ENTITY_TYPE + "/entity" + iteration)
                            .queryParam("query_type", SecretQueryType.LIST_SECRETS.name())
                            .build())
                    .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                    .header("X-ESS-NAMESPACE", PERF_TEST_NS)
                    .exchange()
                    .expectStatus()
                    .isOk()
                    // Drain the response body so Reactor-Netty releases the client-side
                    // ByteBuf. Without this, the undrained buffer is only reclaimed at GC
                    // and Netty's leak detector reports it as a (false-positive) LEAK.
                    .expectBody();
        });
        
        assertTrue(stats.p99() < P99_READ_THRESHOLD_MS,
                "LIST paths p99 latency should be < " + P99_READ_THRESHOLD_MS + " ms, was " + stats.p99() + " ms");
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", stats.totalIterations());
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==============================================
    // PERFORMANCE TESTS: CREATE operations
    // ==============================================

    @Test
    @Order(12)
    void createSecret_withManyTombstonedEntityTypes_p99Performant() {
        int totalIterations = TOTAL_ITERATIONS;
        log.warn("Testing CREATE secret p99 latency with {} tombstoned entity types over {} iterations ({} threads)",
                NUM_ENTITY_TYPES_TO_TOMBSTONE, totalIterations, NUM_THREADS);
        
        LatencyStats stats = measureLatenciesConcurrent("CREATE secret", iteration -> {
            // Each iteration creates a secret in a unique entity
            verifySuccessResponse(createOrUpdateSecret(PERF_TEST_NS, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                    ACTIVE_ENTITY_TYPE + "/createEntity" + iteration + "/secret", sampleSecretData), HttpStatus.OK);
        });
        
        assertTrue(stats.p99() < P99_WRITE_THRESHOLD_MS,
                "CREATE secret p99 latency should be < " + P99_WRITE_THRESHOLD_MS + " ms, was " + stats.p99() + " ms");
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", stats.totalIterations());
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==============================================
    // PERFORMANCE TESTS: DELETE operations
    // ==============================================

    @Test
    @Order(13)
    void deleteSecret_withManyTombstonedEntityTypes_p99Performant() {
        int totalIterations = TOTAL_ITERATIONS;
        log.warn("Testing DELETE secret p99 latency with {} tombstoned entity types over {} iterations ({} threads)",
                NUM_ENTITY_TYPES_TO_TOMBSTONE, totalIterations, NUM_THREADS);
        log.warn("Deleting secrets created in the CREATE test (createEntity0..{}/secret)", totalIterations - 1);
        
        LatencyStats stats = measureLatenciesConcurrent("DELETE secret", iteration -> {
            // Delete the secrets that were created in the CREATE test
            deleteEntityOrSecret(PERF_TEST_NS, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                    ACTIVE_ENTITY_TYPE + "/createEntity" + iteration + "/secret");
        });
        
        assertTrue(stats.p99() < P99_WRITE_THRESHOLD_MS,
                "DELETE secret p99 latency should be < " + P99_WRITE_THRESHOLD_MS + " ms, was " + stats.p99() + " ms");
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", stats.totalIterations());
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==============================================
    // CLEANUP
    // ==============================================

    @Test
    @Order(14)
    void cleanup_deleteNamespace() {
        log.warn("Cleaning up performance test namespace: {}", PERF_TEST_NS);
        deleteNamespace(PERF_TEST_NS);
        
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }
}
