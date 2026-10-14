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
package com.nvidia.icms.service.cpu;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTimeoutPreemptively;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.nvidia.icms.configuration.cpu.CpuWorkloadsConfigurationProperties;
import com.nvidia.icms.errors.CpuWorkloadsNotReadyException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV4Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV3Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import java.time.Duration;
import java.util.List;
import java.util.Set;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Consumer;
import org.junit.jupiter.api.Test;

class CpuWorkloadsServiceTest {

    private static final String CPU_SHAPE = "NCP.CPU.4c16g";

    // ---- builders ----

    private static InstanceTypeV5Udt cpuShape() {
        return InstanceTypeV5Udt.builder().name(CPU_SHAPE).value(CPU_SHAPE).gpuCount(0)
                .cpuCores(4).systemMemory("16Gi").storage("50Gi").isDefault(true).build();
    }

    private static InstanceTypeV5Udt gpuType(int gpuCount) {
        return InstanceTypeV5Udt.builder().name("NCP.GPU.H100_1x").value("NCP.GPU.H100_1x")
                .gpuCount(gpuCount).cpuCores(8).systemMemory("64Gi").isDefault(true).build();
    }

    private static GpuV5Udt gpu(String name, InstanceTypeV5Udt... types) {
        return GpuV5Udt.builder().name(name).capacity(1).instanceTypes(Set.of(types)).build();
    }

    private static ClusterEntity cluster(String id, Set<GpuV5Udt> v5) {
        return ClusterEntity.builder().clusterId(id).gpusV5(v5).build();
    }

    private static CpuWorkloadsService enabledWith(ClusterEntity... clusters) {
        return CpuWorkloadsTestFixtures.enabledWithClusters(List.of(clusters));
    }

    // ---- states ----

    @Test
    void status_disabledByDefault_andNeverReadsClusters() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        ClusterRepository repository = mock(ClusterRepository.class);
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository);

        assertEquals(CpuWorkloadsState.DISABLED, service.status().state());
        assertEquals(CpuWorkloadsStatus.REASON_DISABLED, service.status().reason());
        assertFalse(service.isReady());
        verify(repository, times(0)).forEachClusterPage(anyInt(), any());
    }

    @Test
    void status_ready_whenEnabledAndNoGpuUsesTheReservedName() {
        CpuWorkloadsService service = enabledWith(cluster("c1", Set.of(gpu("H100", gpuType(1)))));

        assertEquals(CpuWorkloadsState.READY, service.status().state());
        assertTrue(service.isReady());
    }

    @Test
    void status_ready_whenReservedNameHoldsOnlyValidCpuShapes() {
        CpuWorkloadsService service = enabledWith(cluster("c1", Set.of(gpu("CPU", cpuShape()))));

        assertTrue(service.isReady());
    }

    @Test
    void status_blocked_whenARealGpuUsesTheReservedNameInAnyCase() {
        CpuWorkloadsService service = enabledWith(cluster("c1", Set.of(gpu("cpu", gpuType(1)))));

        CpuWorkloadsStatus status = service.status();
        assertEquals(CpuWorkloadsState.BLOCKED, status.state());
        assertEquals(CpuWorkloadsStatus.REASON_COLLISION_DETECTED, status.reason());
    }

    @Test
    void status_blocked_whenReservedNameHoldsAGpuInstanceType() {
        CpuWorkloadsService service =
                enabledWith(cluster("c1", Set.of(gpu("CPU", cpuShape(), gpuType(1)))));

        assertEquals(CpuWorkloadsState.BLOCKED, service.status().state());
    }

    @Test
    void status_blocked_whenCollisionIsOnlyInTheLegacyColumn() {
        ClusterEntity legacy = ClusterEntity.builder().clusterId("old")
                .gpus(Set.of(GpuUdt.builder().name("Cpu").instanceTypes(Set.of(
                        InstanceTypeUdt.builder().name("X.GPU.A100_1x").value("X.GPU.A100_1x")
                                .gpuCount(1).cpuCores(8).systemMemory("32Gi").build())).build()))
                .build();

        assertEquals(CpuWorkloadsState.BLOCKED, enabledWith(legacy).status().state());
    }

    @Test
    void status_blocked_whenCollisionIsOnlyInTheV4Column() {
        ClusterEntity v4 = ClusterEntity.builder().clusterId("v4")
                .gpusV4(Set.of(GpuV4Udt.builder().name("CPU").instanceTypes(Set.of(
                        InstanceTypeV3Udt.builder().name("X.GPU.A100_1x").value("X.GPU.A100_1x")
                                .gpuCount(1).cpuCores(8).systemMemory("32Gi").build())).build()))
                .build();

        assertEquals(CpuWorkloadsState.BLOCKED, enabledWith(v4).status().state());
    }

    @Test
    void status_blocked_whenStoredCpuEntryHasZeroOrSeveralShapes() {
        InstanceTypeV5Udt second = InstanceTypeV5Udt.builder().name("NCP.CPU.8c32g")
                .value("NCP.CPU.8c32g").gpuCount(0).cpuCores(8).systemMemory("32Gi").build();

        assertEquals(CpuWorkloadsState.BLOCKED, enabledWith(
                cluster("c1", Set.of(gpu("CPU", cpuShape(), second)))).status().state());
        assertEquals(CpuWorkloadsState.BLOCKED, enabledWith(
                cluster("c2", Set.of(gpu("CPU")))).status().state());
    }

    @Test
    void status_neverNamesAClusterOrGpuInTheReason() {
        CpuWorkloadsStatus status =
                enabledWith(cluster("secret-cluster-id", Set.of(gpu("cpu", gpuType(1))))).status();

        assertFalse(status.reason().contains("secret-cluster-id"));
        assertFalse(status.reason().toLowerCase().contains("cpu"));
    }

    @Test
    void guard_pendingWhenClustersCannotBeRead_andTheBackgroundRetryRecovers() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        doThrow(new RuntimeException("database unavailable"))
                .when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository);

        // Startup tries, fails, and reports a generic pending reason.
        service.onApplicationReady();
        assertEquals(CpuWorkloadsStatus.REASON_GUARD_PENDING, service.status().reason());
        assertEquals(CpuWorkloadsState.BLOCKED, service.status().state());
        verify(repository, times(1)).forEachClusterPage(anyInt(), any());

        // Requests only read the result. They never reach the database.
        service.status();
        service.isReady();
        verify(repository, times(1)).forEachClusterPage(anyInt(), any());

        // The background retry reads again and, once the read works, the state becomes ready.
        doAnswer(invocation -> {
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(List.of());
            return null;
        }).when(repository).forEachClusterPage(anyInt(), any());
        service.retryGuard();
        assertEquals(CpuWorkloadsState.READY, service.status().state());
        verify(repository, times(2)).forEachClusterPage(anyInt(), any());

        // A clean result is never read again.
        service.retryGuard();
        verify(repository, times(2)).forEachClusterPage(anyInt(), any());
    }

    @Test
    void guard_blocked_recoversAfterTheCollisionIsFixedWithoutARestart() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        AtomicReference<List<ClusterEntity>> stored = new AtomicReference<>(
                List.of(cluster("c1", Set.of(gpu("CPU", gpuType(1))))));
        doAnswer(invocation -> {
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(stored.get());
            return null;
        }).when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository);
        service.onApplicationReady();
        assertEquals(CpuWorkloadsStatus.REASON_COLLISION_DETECTED, service.status().reason());

        // Fixed in the data, but only the background retry reads it again: requests stay blocked.
        stored.set(List.of());
        assertEquals(CpuWorkloadsState.BLOCKED, service.status().state());
        verify(repository, times(1)).forEachClusterPage(anyInt(), any());

        service.retryGuard();
        assertEquals(CpuWorkloadsState.READY, service.status().state());
    }

    @Test
    void guard_registrationOfARealGpuUnderTheReservedName_isRefusedWhileAScanRuns() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        AtomicReference<CpuWorkloadsService> serviceRef = new AtomicReference<>();
        AtomicReference<Boolean> refused = new AtomicReference<>(false);
        doAnswer(invocation -> {
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(List.of());
            // A real GPU named cpu is registered while the scan runs. It cannot be stored behind the scan,
            // because it is refused here, before anything is stored.
            try {
                serviceRef.get().validateGpus(Set.of(gpu("cpu", gpuType(1))));
            } catch (IcmsBadRequestException e) {
                refused.set(true);
            }
            return null;
        }).when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository, Runnable::run);
        serviceRef.set(service);

        service.onApplicationReady();

        assertTrue(refused.get());
        // The scan can end clean, and nothing under the reserved name could have been stored.
        assertEquals(CpuWorkloadsState.READY, service.status().state());
    }

    @Test
    void validateGpus_enabledButGuardPending_refusesARealGpuUnderTheReservedName() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        doThrow(new RuntimeException("database unavailable"))
                .when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository, Runnable::run);
        service.onApplicationReady();
        assertEquals(CpuWorkloadsStatus.REASON_GUARD_PENDING, service.status().reason());

        IcmsBadRequestException e = assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("cpu", gpuType(1)))));
        assertTrue(e.getBody().getDetail().contains("is reserved for CPU-only workloads"));
        // GPUs under other names are never affected, and a CPU shape is still refused as not ready.
        assertDoesNotThrow(() -> service.validateGpus(Set.of(gpu("H100", gpuType(1)))));
        assertThrows(CpuWorkloadsNotReadyException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", cpuShape()))));
    }

    @Test
    void guard_scanWithNoConcurrentRegistration_stillBecomesClean() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        doAnswer(invocation -> {
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(List.of());
            return null;
        }).when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository, Runnable::run);

        service.onApplicationReady();

        assertEquals(CpuWorkloadsState.READY, service.status().state());
    }

    @Test
    void onRefresh_doesNotWaitForTheScan() throws Exception {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        CountDownLatch scanStarted = new CountDownLatch(1);
        CountDownLatch releaseScan = new CountDownLatch(1);
        doAnswer(invocation -> {
            scanStarted.countDown();
            releaseScan.await();
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(List.of());
            return null;
        }).when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository);
        try {
            // The refresh returns at once, while the scan is stuck in a slow read.
            assertTimeoutPreemptively(Duration.ofSeconds(2), service::onRefresh);
            assertTrue(scanStarted.await(5, TimeUnit.SECONDS), "the background scan did not start");
            assertTimeoutPreemptively(Duration.ofSeconds(2),
                    () -> assertEquals(CpuWorkloadsState.BLOCKED, service.status().state()));

            // Once the scan ends, the result shows up.
            releaseScan.countDown();
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            while (!service.isReady() && System.nanoTime() < deadline) {
                Thread.sleep(10);
            }
            assertEquals(CpuWorkloadsState.READY, service.status().state());
        } finally {
            releaseScan.countDown();
            service.destroy();
        }
    }

    @Test
    void retryGuard_doesNothingWhenTheSwitchIsOff() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        ClusterRepository repository = mock(ClusterRepository.class);
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository);

        service.retryGuard();

        verify(repository, times(0)).forEachClusterPage(anyInt(), any());
    }

    @Test
    void status_doesNotWaitForAGuardScanThatIsRunning() throws Exception {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        CountDownLatch scanStarted = new CountDownLatch(1);
        CountDownLatch releaseScan = new CountDownLatch(1);
        // The first read fails, so the result is not clean. The background retry then blocks inside the
        // read, as a slow database would.
        doThrow(new RuntimeException("database unavailable"))
                .doAnswer(invocation -> {
                    scanStarted.countDown();
                    releaseScan.await();
                    return null;
                })
                .when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository);
        service.onApplicationReady();

        ExecutorService executor = Executors.newSingleThreadExecutor();
        try {
            Future<?> scan = executor.submit(service::retryGuard);
            assertTrue(scanStarted.await(5, TimeUnit.SECONDS), "the background scan did not start");

            // While the scan is stuck, requests still get the cached result at once, whichever
            // entry point they use.
            assertTimeoutPreemptively(Duration.ofSeconds(2), () -> {
                assertEquals(CpuWorkloadsState.BLOCKED, service.status().state());
                assertFalse(service.isReady());
            });

            releaseScan.countDown();
            scan.get(5, TimeUnit.SECONDS);
        } finally {
            releaseScan.countDown();
            executor.shutdownNow();
        }
    }

    @Test
    void validateGpus_ready_rejectsACpuEntryWithNoShape() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.ready();

        assertThrows(IcmsBadRequestException.class, () -> service.validateGpus(Set.of(gpu("CPU"))));
    }

    @Test
    void validateGpus_ready_acceptsOnlyOneCpuShape() {
        InstanceTypeV5Udt second = InstanceTypeV5Udt.builder().name("NCP.CPU.8c32g")
                .value("NCP.CPU.8c32g").gpuCount(0).cpuCores(8).systemMemory("32Gi").build();
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.ready();

        assertThrows(IcmsBadRequestException.class,
                     () -> service.validateGpus(Set.of(gpu("CPU", cpuShape(), second))));
    }

    @Test
    void refresh_reevaluatesAfterTheStoredDataChanges() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        AtomicReference<List<ClusterEntity>> stored = new AtomicReference<>(List.of());
        doAnswer(invocation -> {
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(stored.get());
            return null;
        }).when(repository).forEachClusterPage(anyInt(), any());
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository, Runnable::run);
        service.onApplicationReady();
        assertTrue(service.isReady());

        stored.set(List.of(cluster("c1", Set.of(gpu("cpu", gpuType(1))))));
        service.onRefresh();

        assertEquals(CpuWorkloadsState.BLOCKED, service.status().state());
    }

    @Test
    void refresh_whenTurnedOff_returnsToDisabled() {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(true);
        ClusterRepository repository = mock(ClusterRepository.class);
        CpuWorkloadsService service = new CpuWorkloadsService(properties, repository, Runnable::run);
        service.onApplicationReady();
        assertTrue(service.isReady());

        properties.setEnabled(false);
        service.onRefresh();

        assertEquals(CpuWorkloadsState.DISABLED, service.status().state());
    }

    // ---- registration rules ----

    @Test
    void validateGpus_disabled_rejectsCpuShapes() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.disabled();

        CpuWorkloadsNotReadyException e = assertThrows(CpuWorkloadsNotReadyException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", cpuShape()))));
        assertTrue(e.getBody().getDetail().startsWith(CpuWorkloadsNotReadyException.ERROR_CODE));
    }

    @Test
    void validateGpus_disabled_allowsGpusAndExistingEntriesUnderTheReservedName() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.disabled();

        // Existing behavior is unchanged: ordinary GPUs, a zero count under another name, and a
        // real GPU that already uses the reserved name.
        assertDoesNotThrow(() -> service.validateGpus(Set.of(gpu("H100", gpuType(1)))));
        assertDoesNotThrow(() -> service.validateGpus(Set.of(gpu("H100", gpuType(0)))));
        assertDoesNotThrow(() -> service.validateGpus(Set.of(gpu("CPU", gpuType(1)))));
    }

    @Test
    void validateGpus_blocked_refusesCpuShapesAndRealGpusUnderTheReservedName() {
        CpuWorkloadsService service =
                enabledWith(cluster("c1", Set.of(gpu("cpu", gpuType(1)))));

        assertThrows(CpuWorkloadsNotReadyException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", cpuShape()))));
        // The name is reserved while CPU workloads are enabled, so the GPU that blocks them must be
        // renamed. Other GPUs are not affected.
        assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("cpu", gpuType(1)))));
        assertDoesNotThrow(() -> service.validateGpus(Set.of(gpu("H100", gpuType(1)))));
    }

    @Test
    void validateGpus_ready_acceptsAValidCpuShape() {
        assertDoesNotThrow(() -> CpuWorkloadsTestFixtures.ready()
                .validateGpus(Set.of(gpu("CPU", cpuShape()))));
    }

    @Test
    void validateGpus_ready_rejectsNonCanonicalReservedName() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.ready();

        assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("cpu", cpuShape()))));
    }

    @Test
    void validateGpus_ready_rejectsGpuInstanceTypeUnderTheReservedName() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.ready();

        assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", gpuType(1)))));
        assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", cpuShape(), gpuType(1)))));
    }

    @Test
    void validateGpus_ready_rejectsShapeMissingCoresOrMemory() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.ready();
        InstanceTypeV5Udt noCores = cpuShape();
        noCores.setCpuCores(0);
        InstanceTypeV5Udt noMemory = cpuShape();
        noMemory.setSystemMemory(null);

        assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", noCores))));
        assertThrows(IcmsBadRequestException.class,
                () -> service.validateGpus(Set.of(gpu("CPU", noMemory))));
    }

    @Test
    void validateGpus_ready_rejectsShapesNvcaCouldNotBuild() {
        CpuWorkloadsService service = CpuWorkloadsTestFixtures.ready();
        InstanceTypeV5Udt mismatchedValue = cpuShape();
        mismatchedValue.setValue("NCP.CPU.8c32g");
        InstanceTypeV5Udt badMemory = cpuShape();
        badMemory.setSystemMemory("lots");
        InstanceTypeV5Udt zeroMemory = cpuShape();
        zeroMemory.setSystemMemory("0Gi");
        InstanceTypeV5Udt noStorage = cpuShape();
        noStorage.setStorage(null);
        InstanceTypeV5Udt badStorage = cpuShape();
        badStorage.setStorage("big");
        InstanceTypeV5Udt zeroStorage = cpuShape();
        zeroStorage.setStorage("0");
        InstanceTypeV5Udt badLabel = cpuShape();
        badLabel.setName("NCP.CPU.bad-");
        badLabel.setValue("NCP.CPU.bad-");

        for (InstanceTypeV5Udt shape : List.of(
                mismatchedValue, badMemory, zeroMemory, noStorage, badStorage, zeroStorage, badLabel)) {
            // The message is the CPU shape validator's own, so the entry was rejected there and not
            // by an earlier check.
            IcmsBadRequestException e = assertThrows(IcmsBadRequestException.class,
                    () -> service.validateGpus(Set.of(gpu("CPU", shape))),
                    shape.toString());
            assertTrue(e.getMessage().contains("is reserved for CPU-only workloads")
                    && e.getMessage().contains("Instance type '" + shape.getName() + "' does not qualify"),
                    e.getMessage());
        }
        assertDoesNotThrow(() -> service.validateGpus(Set.of(gpu("CPU", cpuShape()))));
    }

    @Test
    void validateGpus_ready_leavesOtherGpusAlone() {
        assertDoesNotThrow(() -> CpuWorkloadsTestFixtures.ready()
                .validateGpus(Set.of(gpu("H100", gpuType(1)), gpu("A100", gpuType(0)))));
    }

    @Test
    void validateLegacyGpus_followsTheSameRules() {
        GpuUdt cpuShapeLegacy = GpuUdt.builder().name("CPU").instanceTypes(Set.of(
                InstanceTypeUdt.builder().name(CPU_SHAPE).value(CPU_SHAPE).gpuCount(0)
                        .cpuCores(4).systemMemory("16Gi").build())).build();

        assertThrows(CpuWorkloadsNotReadyException.class,
                () -> CpuWorkloadsTestFixtures.disabled().validateLegacyGpus(Set.of(cpuShapeLegacy)));
        assertDoesNotThrow(
                () -> CpuWorkloadsTestFixtures.ready().validateLegacyGpus(Set.of(cpuShapeLegacy)));
    }

    // ---- destination rule ----

    @Test
    void assertDestinationAllowed_rejectsCpuShapeUnlessReady() {
        assertThrows(CpuWorkloadsNotReadyException.class, () -> CpuWorkloadsTestFixtures.disabled()
                .assertDestinationAllowed("CPU", cpuShape()));
        assertThrows(CpuWorkloadsNotReadyException.class,
                () -> enabledWith(cluster("c1", Set.of(gpu("cpu", gpuType(1)))))
                        .assertDestinationAllowed("CPU", cpuShape()));
        assertDoesNotThrow(() -> CpuWorkloadsTestFixtures.ready()
                .assertDestinationAllowed("CPU", cpuShape()));
    }

    @Test
    void assertDestinationAllowed_neverAffectsGpuInstanceTypes() {
        // A zero count under an ordinary GPU name, and a real GPU under the reserved name, keep
        // today's behavior in every state.
        assertDoesNotThrow(() -> CpuWorkloadsTestFixtures.disabled()
                .assertDestinationAllowed("H100", gpuType(0)));
        assertDoesNotThrow(() -> CpuWorkloadsTestFixtures.disabled()
                .assertDestinationAllowed("CPU", gpuType(1)));
    }
}
