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

import com.nvidia.icms.configuration.cpu.CpuWorkloadsConfigurationProperties;
import com.nvidia.icms.errors.CpuWorkloadsNotReadyException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.Executor;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.DisposableBean;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.event.EventListener;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

/**
 * Decides whether CPU-only workloads are supported and enforces the rules that keep them apart
 * from GPU workloads.
 *
 * <p>States: {@link CpuWorkloadsState#DISABLED} when the switch is off (default, no behavior
 * change anywhere), {@link CpuWorkloadsState#READY} when the switch is on and the collision guard
 * found no GPU registered under the reserved name, and {@link CpuWorkloadsState#BLOCKED}
 * otherwise. Outside READY, CPU shapes are rejected explicitly and are never processed as GPU
 * instance types.
 */
@Service
@Slf4j
public class CpuWorkloadsService implements DisposableBean {

    private static final String RESERVED_NAME_MESSAGE =
            "GPU name '%s' is reserved for CPU-only workloads. Register it only as 'CPU' with "
                    + "instance types named <provider>.CPU.<shape> (name and value the same, and a "
                    + "valid label value), gpuCount 0 and positive cpuCores, systemMemory and storage. "
                    + "Instance type '%s' does not qualify.";

    private static final String RESERVED_WHILE_ENABLED_MESSAGE =
            "GPU name '%s' is reserved for CPU-only workloads while they are enabled on this ICMS, and "
                    + "instance type '%s' does not qualify. Rename the GPU before registering it, or turn "
                    + "CPU workloads off.";

    private static final String ONE_SHAPE_MESSAGE =
            "GPU name '%s' requires exactly one CPU shape per cluster in this release.";

    private enum GuardResult { UNEVALUATED, CLEAN, COLLISION }

    /**
     * One registered instance type. The legacy registration forms carry no storage, so they set
     * {@code storageDeclared} false and only the forms that have the field are required to give it.
     */
    private record InstanceView(
            String name, String value, int gpuCount, int cpuCores, String systemMemory,
            String storage, boolean storageDeclared) { }

    private record GpuView(String name, List<InstanceView> instanceTypes) { }

    private final CpuWorkloadsConfigurationProperties properties;
    private final ClusterRepository clusterRepository;
    // Runs the guard after a configuration refresh, so the refresh thread never waits for the scan.
    private final Executor refreshExecutor;
    private final AtomicBoolean refreshQueued = new AtomicBoolean();

    // Written only by the collision guard, read by every request without a lock.
    private volatile GuardResult guardResult = GuardResult.UNEVALUATED;


    @Autowired
    public CpuWorkloadsService(
            CpuWorkloadsConfigurationProperties properties, ClusterRepository clusterRepository) {
        this(properties, clusterRepository, Executors.newSingleThreadExecutor(runnable -> {
            Thread thread = new Thread(runnable, "cpu-workloads-guard");
            thread.setDaemon(true);
            return thread;
        }));
    }

    CpuWorkloadsService(
            CpuWorkloadsConfigurationProperties properties,
            ClusterRepository clusterRepository,
            Executor refreshExecutor) {
        this.properties = properties;
        this.clusterRepository = clusterRepository;
        this.refreshExecutor = refreshExecutor;
    }

    @Override
    public void destroy() {
        if (refreshExecutor instanceof ExecutorService executorService) {
            executorService.shutdownNow();
        }
    }

    /**
     * Current status. It returns the last result of the collision guard and never reads cluster
     * records, so a request never waits for the guard. The guard runs at startup, after a refresh, and
     * in the background while its result is not clean ({@link #retryGuard()}).
     */
    public CpuWorkloadsStatus status() {
        if (!properties.isEnabled()) {
            return CpuWorkloadsStatus.DISABLED;
        }
        return switch (guardResult) {
            case CLEAN -> CpuWorkloadsStatus.READY;
            case COLLISION -> CpuWorkloadsStatus.blocked(CpuWorkloadsStatus.REASON_COLLISION_DETECTED);
            case UNEVALUATED -> CpuWorkloadsStatus.blocked(CpuWorkloadsStatus.REASON_GUARD_PENDING);
        };
    }

    public boolean isReady() {
        return status().isReady();
    }

    @EventListener(ApplicationReadyEvent.class)
    public void onApplicationReady() {
        reevaluate();
    }

    /**
     * A configuration refresh runs on the publisher's thread, so the scan is handed to a background
     * thread. Until it finishes, requests keep reading the previous result.
     */
    @EventListener(RefreshScopeRefreshedEvent.class)
    public void onRefresh() {
        if (!properties.isEnabled()) {
            guardResult = GuardResult.UNEVALUATED;
            return;
        }
        if (refreshQueued.compareAndSet(false, true)) {
            refreshExecutor.execute(() -> {
                refreshQueued.set(false);
                evaluateGuard();
            });
        }
    }

    private void reevaluate() {
        if (properties.isEnabled()) {
            evaluateGuard();
        } else {
            guardResult = GuardResult.UNEVALUATED;
        }
    }

    /**
     * Runs the collision guard again in the background while its result is not clean, so the state
     * recovers once the collision is fixed or the cluster records become readable, without an ICMS
     * restart. A clean result is never read again, and a disabled switch does nothing.
     */
    @Scheduled(initialDelayString = "${icms.cpu-workloads.guard-retry-interval:PT30S}",
            fixedDelayString = "${icms.cpu-workloads.guard-retry-interval:PT30S}")
    public void retryGuard() {
        if (properties.isEnabled() && guardResult != GuardResult.CLEAN) {
            evaluateGuard();
        }
    }

    /**
     * Reads ICMS's own stored cluster records and looks for a GPU registered under the reserved
     * name that is not a valid CPU shape. Details go to the log only.
     */
    private synchronized void evaluateGuard() {
        try {
            List<String> collisions = new ArrayList<>();
            clusterRepository.forEachClusterPage(
                    properties.getGuardPageSize(),
                    page -> page.forEach(cluster -> findCollision(cluster).ifPresent(collisions::add)));
            if (collisions.isEmpty()) {
                guardResult = GuardResult.CLEAN;
                log.info("CPU workloads guard passed: no GPU is registered under the reserved name");
            } else {
                guardResult = GuardResult.COLLISION;
                collisions.forEach(c -> log.error(
                        "CPU workloads are blocked: {}. Rename or remove that GPU registration "
                                + "before enabling CPU workloads.", c));
            }
        } catch (RuntimeException e) {
            guardResult = GuardResult.UNEVALUATED;
            log.error("CPU workloads guard could not read cluster records, will retry", e);
        }
    }

    private static Optional<String> findCollision(ClusterEntity cluster) {
        List<GpuView> gpus = new ArrayList<>();
        cluster.getGpus().forEach(g -> gpus.add(new GpuView(g.getName(), g.getInstanceTypes().stream()
                .map(i -> new InstanceView(i.getName(), i.getValue(), i.getGpuCount(),
                                           i.getCpuCores(), i.getSystemMemory(), null, false)).toList())));
        cluster.getGpusV4().forEach(g -> gpus.add(new GpuView(g.getName(), g.getInstanceTypes().stream()
                .map(i -> new InstanceView(i.getName(), i.getValue(), i.getGpuCount(),
                                           i.getCpuCores(), i.getSystemMemory(), null, false)).toList())));
        cluster.getGpusV5().forEach(g -> gpus.add(toView(g)));
        for (GpuView gpu : gpus) {
            if (!CpuShapes.isReservedGpuName(gpu.name())) {
                continue;
            }
            Optional<InstanceView> offending = offendingInstance(gpu);
            // Stored entries must already meet what registration now requires: the canonical name
            // and exactly one valid CPU shape. Anything else is a collision.
            if (offending.isPresent() || !CpuShapes.isCanonicalGpuName(gpu.name())
                    || gpu.instanceTypes().size() != 1) {
                return Optional.of(String.format(
                        "cluster %s has GPU '%s' registered under the reserved name",
                        cluster.getClusterId(),
                        gpu.name()));
            }
        }
        return Optional.empty();
    }

    private static Optional<InstanceView> offendingInstance(GpuView gpu) {
        return gpu.instanceTypes().stream().filter(i -> !isValidShape(i)).findFirst();
    }

    private static boolean isValidShape(InstanceView i) {
        return i.storageDeclared()
                ? CpuShapes.isValidShape(
                        i.name(), i.value(), i.gpuCount(), i.cpuCores(), i.systemMemory(), i.storage())
                : CpuShapes.isValidShape(i.name(), i.value(), i.gpuCount(), i.cpuCores(), i.systemMemory());
    }

    /**
     * Validates GPUs being registered or reconfigured, current (v5) form.
     *
     * <p>READY: a GPU named {@code CPU} in any letter case must be exactly {@code CPU} and have
     * only valid CPU shapes. Other states: any CPU shape is rejected with
     * {@link CpuWorkloadsNotReadyException}. While the switch is on but the guard is not ready, an
     * entry under the reserved name that is not a CPU shape is refused too, so that nothing under that
     * name can be stored behind a running scan. With the switch off, nothing changes: GPUs under other
     * names, and non-shape entries under the reserved name, are not affected. GPUs under other names
     * are never affected.
     */
    public void validateGpus(Collection<GpuV5Udt> gpus) {
        validateViews(gpus.stream().map(CpuWorkloadsService::toView).toList());
    }

    /** Same validation for the legacy registration form used by the BART flow. */
    public void validateLegacyGpus(Collection<GpuUdt> gpus) {
        validateViews(gpus.stream().map(g -> new GpuView(g.getName(), g.getInstanceTypes().stream()
                .map(i -> new InstanceView(i.getName(), i.getValue(), i.getGpuCount(),
                                           i.getCpuCores(), i.getSystemMemory(), null, false)).toList())).toList());
    }

    private void validateViews(List<GpuView> gpus) {
        if (gpus.stream().noneMatch(g -> CpuShapes.isReservedGpuName(g.name()))) {
            return;
        }
        CpuWorkloadsStatus status = status();
        for (GpuView gpu : gpus) {
            if (!CpuShapes.isReservedGpuName(gpu.name())) {
                continue;
            }
            if (status.isReady()) {
                requireReservedNameForCpuShapesOnly(gpu);
            } else if (gpu.instanceTypes().stream().anyMatch(
                    i -> CpuShapes.isCpuShapeEntry(gpu.name(), i.name(), i.gpuCount()))) {
                throw new CpuWorkloadsNotReadyException(status);
            } else if (properties.isEnabled()) {
                // The switch is on and the guard has not reported a clean result, so nothing under the
                // reserved name may be stored: a real GPU stored now could land behind a scan that has
                // already read its cluster, and the result would stay clean while it exists. The name is
                // reserved while CPU workloads are enabled, so such a GPU must be renamed first.
                throw new IcmsBadRequestException(String.format(
                        RESERVED_WHILE_ENABLED_MESSAGE, gpu.name(),
                        gpu.instanceTypes().stream().map(InstanceView::name).findFirst().orElse("any")));
            }
        }
    }

    private static void requireReservedNameForCpuShapesOnly(GpuView gpu) {
        if (!CpuShapes.isCanonicalGpuName(gpu.name())) {
            throw new IcmsBadRequestException(
                    String.format(RESERVED_NAME_MESSAGE, gpu.name(), "any"));
        }
        if (gpu.instanceTypes().size() != 1) {
            throw new IcmsBadRequestException(String.format(ONE_SHAPE_MESSAGE, gpu.name()));
        }
        offendingInstance(gpu).ifPresent(i -> {
            throw new IcmsBadRequestException(
                    String.format(RESERVED_NAME_MESSAGE, gpu.name(), i.name()));
        });
    }

    /**
     * Rejects a request whose destination is a CPU shape while CPU workloads are not ready, so
     * that it is never processed as a GPU instance type.
     */
    public void assertDestinationAllowed(String gpuName, InstanceTypeV5Udt instanceType) {
        if (!CpuShapes.isCpuShapeEntry(gpuName, instanceType.getName(), instanceType.getGpuCount())) {
            return;
        }
        CpuWorkloadsStatus status = status();
        if (!status.isReady()) {
            throw new CpuWorkloadsNotReadyException(status);
        }
    }

    private static GpuView toView(GpuV5Udt gpu) {
        return new GpuView(gpu.getName(), gpu.getInstanceTypes().stream()
                .map(i -> new InstanceView(i.getName(), i.getValue(), i.getGpuCount(),
                                           i.getCpuCores(), i.getSystemMemory(), i.getStorage(), true)).toList());
    }
}
