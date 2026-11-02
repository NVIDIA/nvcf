/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.outbound.cassandra.byoc;

import static org.junit.jupiter.api.Assertions.assertEquals;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.beans.factory.annotation.Autowired;

class NvcaRegistrationVersionIntegrationTest extends IntegrationTest {
    @Autowired
    private ClusterByIdRepo rows;

    @Autowired
    private NvcaClusterRepository registrations;

    @Test
    void staleRegistrationCannotReplaceConfiguredVersion() {
        var original = cluster("1.0.0", "ngc-managed");
        rows.update(original);
        var registration = original.toBuilder().k8sVersion("1.30.0").build();
        rows.update(original.toBuilder().nvcaVersion("1.1.0").build());

        registrations.updateClusterRegistration(registration);

        var saved = rows.findById(original.getClusterId()).orElseThrow();
        assertEquals("1.1.0", saved.getNvcaVersion());
        assertEquals("1.30.0", saved.getK8sVersion());
        assertEquals("1.0.0", registration.getNvcaVersion());
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" "})
    void initializeLegacyVersionOnlyWhileItsPreviousValueIsUnchanged(String previousVersion) {
        var original = cluster(previousVersion, "ngc-managed");
        rows.update(original);
        var registration = original.toBuilder().nvcaVersion("1.0.0").build();

        registrations.updateClusterRegistration(registration, previousVersion);

        assertEquals("1.0.0", rows.findById(original.getClusterId())
                .orElseThrow().getNvcaVersion());
    }

    @Test
    void legacyInitializationCannotReplaceAConfiguredTarget() {
        var original = cluster(null, "ngc-managed");
        rows.update(original);
        var registration = original.toBuilder().nvcaVersion("1.0.0").build();
        rows.update(original.toBuilder().nvcaVersion("1.1.0").build());

        registrations.updateClusterRegistration(registration, null);

        assertEquals("1.1.0", rows.findById(original.getClusterId())
                .orElseThrow().getNvcaVersion());
    }

    @Test
    void helmManagedRegistrationContinuesUpdatingReportedVersion() {
        var original = cluster("1.0.0", "helm-managed");
        rows.update(original);

        registrations.updateClusterRegistration(original.toBuilder().nvcaVersion("1.1.0").build());

        assertEquals("1.1.0", rows.findById(original.getClusterId())
                .orElseThrow().getNvcaVersion());
    }

    @Test
    void heartbeatSnapshotCannotReplaceConfiguredVersion() {
        var original = cluster("1.0.0", "ngc-managed");
        rows.update(original);
        rows.update(original.toBuilder().nvcaVersion("1.1.0").build());

        registrations.updateClusterEntity(original);

        assertEquals("1.1.0", rows.findById(original.getClusterId())
                .orElseThrow().getNvcaVersion());
    }

    private static ClusterEntity cluster(String version, String source) {
        return ClusterEntity.builder().clusterId(UUID.randomUUID().toString())
                .clusterSource(source).nvcaVersion(version).build();
    }
}
