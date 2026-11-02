/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.service.byoc.nvca;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterByIdRepo;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocServiceHelper;
import com.nvidia.icms.service.byoc.ClusterQueueAccessCredsService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.CopyUtil;
import java.time.Instant;
import java.util.HashMap;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class NvcaUpgradeVersionTest {
    private static final String OLD_VERSION = "1.0.0";
    private static final String NEW_VERSION = "1.1.0";

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"ngc-managed", "NGC-MANAGED", " "})
    void pendingUpgradeSurvivesOldAgentRegistration(String source) {
        assertRegistrationVersion(NEW_VERSION, OLD_VERSION, source, NEW_VERSION);
    }

    @Test
    void pendingDowngradeSurvivesNewerAgentRegistration() {
        assertRegistrationVersion(OLD_VERSION, NEW_VERSION, "ngc-managed", OLD_VERSION);
    }

    @ParameterizedTest
    @ValueSource(strings = {"helm-managed", "self-managed"})
    void externallyManagedClustersContinueReportingRunningVersion(String source) {
        assertRegistrationVersion(OLD_VERSION, NEW_VERSION, source, NEW_VERSION);
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" "})
    void legacyVersionIsInitializedFromRegistration(String previousVersion) {
        assertRegistrationVersion(previousVersion, OLD_VERSION, "ngc-managed", OLD_VERSION);
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" "})
    void missingReportedVersionPreservesConfiguredTarget(String reported) {
        assertRegistrationVersion(NEW_VERSION, reported, "ngc-managed", NEW_VERSION);
    }

    @Test
    void missingReportedVersionDoesNotInitializeLegacyRecord() {
        assertRegistrationVersion(null, null, "ngc-managed", null);
    }

    @Test
    void upgradeSavedAfterRegistrationReadSurvivesStaleEntityWrite() {
        var stored = new AtomicReference<>(cluster(OLD_VERSION, "ngc-managed"));
        var service = service(stored,
                () -> stored.set(cluster(NEW_VERSION, "ngc-managed")));
        register(service, OLD_VERSION);
        assertEquals(NEW_VERSION, stored.get().getNvcaVersion());
    }

    @Test
    void legacyInitializationDoesNotReplaceTargetSavedAfterRegistrationRead() {
        var stored = new AtomicReference<>(cluster(null, "ngc-managed"));
        var service = service(stored,
                () -> stored.set(cluster(NEW_VERSION, "ngc-managed")));
        register(service, OLD_VERSION);
        assertEquals(NEW_VERSION, stored.get().getNvcaVersion());
    }

    @Test
    void registrationOmitsConfiguredVersionWithoutMutatingCaller() {
        var ids = mock(ClusterByIdRepo.class);
        var repository = new NvcaClusterRepository(ids, mock(ClusterRepository.class));
        var entity = cluster(NEW_VERSION, "ngc-managed");
        repository.updateClusterRegistration(entity);
        assertEquals(NEW_VERSION, entity.getNvcaVersion());
        verify(ids).update(argThat(e -> e.getNvcaVersion() == null
                && e.getClusterId().equals(entity.getClusterId())));
        verify(ids, never()).initializeNvcaVersion(any(), any(), any());
    }

    @ParameterizedTest
    @ValueSource(strings = {"ngc-managed", "helm-managed"})
    void heartbeatStatusUpdateOmitsVersionWithoutMutatingCaller(String source) {
        var ids = mock(ClusterByIdRepo.class);
        var repository = new NvcaClusterRepository(ids, mock(ClusterRepository.class));
        var entity = cluster(OLD_VERSION, source);
        repository.updateClusterEntity(entity);
        assertEquals(OLD_VERSION, entity.getNvcaVersion());
        verify(ids).update(argThat(e -> e.getNvcaVersion() == null));
    }

    private static void assertRegistrationVersion(
            String configured, String reported, String source, String expected) {
        var stored = new AtomicReference<>(cluster(configured, source));
        register(service(stored, () -> {}), reported);
        assertEquals(expected, stored.get().getNvcaVersion());
        assertEquals("1.30.0", stored.get().getK8sVersion());
    }

    private static NvcaClusterRegistrationService service(
            AtomicReference<ClusterEntity> stored, Runnable beforeWrite) {
        var ids = mock(ClusterByIdRepo.class);
        var reader = mock(ClusterRepository.class);
        when(reader.getClusterInfoByClusterId(anyString(), eq(false)))
                .thenAnswer(invocation -> Optional.of(CopyUtil.deepCopy(stored.get())));
        doAnswer(invocation -> {
            beforeWrite.run();
            ClusterEntity update = CopyUtil.deepCopy(invocation.getArgument(0));
            // Match the database adapter's withInsertNulls(false) behavior.
            if (update.getNvcaVersion() == null) {
                update.setNvcaVersion(stored.get().getNvcaVersion());
            }
            stored.set(update);
            return update;
        }).when(ids).update(any(ClusterEntity.class));
        when(ids.initializeNvcaVersion(any(), any(), any())).thenAnswer(invocation -> {
            if (!Objects.equals(stored.get().getNvcaVersion(), invocation.getArgument(2))) {
                return false;
            }
            stored.get().setNvcaVersion(invocation.getArgument(1));
            return true;
        });
        var repository = new NvcaClusterRepository(ids, reader);
        var helper = mock(InstanceServiceHelper.class);
        when(helper.isNatsEnabled()).thenReturn(true);
        when(helper.getCurrentTimestamp()).thenReturn(Instant.parse("2026-01-01T00:00:00Z"));
        var credentials = mock(ClusterQueueAccessCredsService.class);
        when(credentials.generateCredsForNvcaQueues(any()))
                .thenReturn(NvcaAccessCreds.builder().build());
        return new NvcaClusterRegistrationService(repository,
                mock(NvcaConfigurationProperties.class), credentials,
                mock(ByocConfigurationProperties.class), reader,
                mock(ClusterOidcIdentityService.class), mock(QueueManager.class),
                mock(TelemetryEventClient.class), mock(AppAuditService.class), helper,
                mock(ByocServiceHelper.class));
    }

    private static ClusterEntity cluster(String version, String source) {
        return ClusterEntity.builder().clusterId("version-test").clusterName("version-test")
                .clusterGroupId("group").clusterGroupName("group").ncaId("account")
                .clusterProvider(ClusterProviderEnum.GDN).clusterStatus(ClusterStatusEnum.READY)
                .clusterSource(source).nvcaVersion(version)
                .capabilities(Set.of()).attributes(Set.of()).authorizedNcaIds(Set.of())
                .gpusV4(Set.of()).gpusV5(Set.of()).build();
    }

    private static void register(NvcaClusterRegistrationService service, String version) {
        service.nvcaClusterRegistration(NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY).k8sVersion("1.30.0").nvcaVersion(version).build(),
                "version-test", new HashMap<>());
    }
}
