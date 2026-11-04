/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.service.byoc.nvca;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class ClusterOidcIdentityService {

    private final ClusterRepository clusterRepository;
    private final NvcaClusterRepository nvcaClusterRepository;

    public Optional<ClusterEntity> findByClusterId(String clusterId) {
        return clusterRepository.getClusterInfoByClusterId(clusterId, false);
    }

    public Optional<ClusterEntity> findByFingerprint(String fingerprint) {
        if (StringUtils.isBlank(fingerprint)) {
            return Optional.empty();
        }
        return clusterRepository.getAllClusters().stream()
                .filter(cluster -> fingerprint.equals(cluster.getJwksFingerprint()))
                .findFirst();
    }

    public void validateFingerprintAvailable(String fingerprint, String clusterId) {
        findByFingerprint(fingerprint)
                .filter(existing -> !clusterId.equals(existing.getClusterId()))
                .ifPresent(existing -> {
                    throw new IcmsConflictException(String.format(
                            "JWKS signing keys are already registered by cluster %s. "
                                    + "Each cluster must have unique signing keys.",
                            existing.getClusterId()));
                });
    }

    public void applyOidcIdentity(
            ClusterEntity clusterEntity,
            String jwks,
            String oidcIssuer,
            String jwksFingerprint) {
        clusterEntity.setJwks(jwks);
        clusterEntity.setOidcIssuer(oidcIssuer);
        clusterEntity.setJwksFingerprint(jwksFingerprint);
    }

    public void updateOidcIdentity(
            String clusterId,
            String jwks,
            String oidcIssuer,
            String jwksFingerprint) {
        ClusterEntity clusterEntity = findByClusterId(clusterId)
                .orElseThrow(() -> new IcmsNotFoundException(
                        String.format("Cluster %s not found", clusterId)));
        applyOidcIdentity(clusterEntity, jwks, oidcIssuer, jwksFingerprint);
        nvcaClusterRepository.updateClusterEntity(clusterEntity);
    }
}
