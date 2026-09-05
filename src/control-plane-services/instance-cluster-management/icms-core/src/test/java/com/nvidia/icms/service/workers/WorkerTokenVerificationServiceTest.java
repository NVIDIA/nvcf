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

package com.nvidia.icms.service.workers;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierKey;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierUdt;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService.RejectReason;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class WorkerTokenVerificationServiceTest {

    private static final String CLUSTER_ID = "cl-abc123";
    private static final String OTHER_CLUSTER_ID = "cl-other";
    private static final String INSTANCE_ID = "inst-789";
    private static final String REQUEST_ID = "sr-req-1";
    private static final String NAMESPACE = "inst-789";
    private static final String POD_NAME = "utils";
    private static final String POD_UID = "f1e2d3c4-5b6a-7980-1234-aabbccddeeff";
    private static final String SA_UID = "7c1f0b2a-9d4e-4a8c-bb21-0f3e5d6a1c22";
    private static final String SA_SUB = "system:serviceaccount:" + NAMESPACE + ":nvcf-worker";
    private static final String AUDIENCE = "nvcf-icms:" + CLUSTER_ID;
    private static final String SPIFFE_SUB = "spiffe://domain/cluster/" + CLUSTER_ID
            + "/instance/" + INSTANCE_ID + "/worker/worker-uuid-001";
    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final UUID FUNCTION_VERSION_ID = UUID.randomUUID();
    private static final Instant EXP = Instant.now().plusSeconds(900);

    @Mock
    private ClusterOIDCTokenVerificationService nvcaTokenVerificationService;

    @Mock
    private WorkerIdentifierService workerIdentifierService;

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    private WorkerTokenVerificationService service;

    @BeforeEach
    void setUp() {
        service = new WorkerTokenVerificationService(nvcaTokenVerificationService,
                workerIdentifierService, instanceV2Repository, instanceRequestV2Repository);
    }

    // --- base verification delegation ---

    @Test
    void verify_baseRejectsToken_propagatesRejection() {
        when(nvcaTokenVerificationService.verify(eq("tok"), any()))
                .thenReturn(ClusterOIDCTokenVerificationService.Outcome.reject(
                        RejectReason.SIGNATURE_INVALID, "JWT verification failed"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertEquals("JWT verification failed", outcome.getErrorMessage());
        verify(workerIdentifierService, never()).findWorkerIdentifiersBySaUid(any(), any());
    }

    @Test
    void verify_unknownCluster_propagatesUnknownClusterReason() {
        when(nvcaTokenVerificationService.verify(eq("tok"), any()))
                .thenReturn(ClusterOIDCTokenVerificationService.Outcome.reject(
                        RejectReason.UNKNOWN_CLUSTER, "No cluster found"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertEquals(RejectReason.UNKNOWN_CLUSTER, outcome.getReason());
    }

    // --- PSAT flow ---

    @Test
    void verify_psatHappyPath_returnsActiveWithBinding() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, POD_UID));
        stubInstanceAndRequest(CLUSTER_ID);

        var outcome = service.verify("tok");

        assertTrue(outcome.isActive());
        assertEquals(CLUSTER_ID, outcome.getClusterId());
        assertEquals(INSTANCE_ID, outcome.getInstanceId());
        assertEquals(POD_NAME, outcome.getWorkerId());
        assertEquals(WorkerTokenVerificationService.TOKEN_TYPE_PSAT, outcome.getTokenType());
        assertEquals(REQUEST_ID, outcome.getRequestId());
        assertEquals(FUNCTION_ID.toString(), outcome.getFunctionId());
        assertEquals(FUNCTION_VERSION_ID.toString(), outcome.getFunctionVersionId());
        assertNull(outcome.getTaskId());
        assertEquals("nca-1", outcome.getNcaId());
        assertEquals(EXP.getEpochSecond(), outcome.getExp());
    }

    @Test
    void verify_psatSubNotWorkerServiceAccount_returnsInactive() {
        stubBaseActive(psatJwt("system:serviceaccount:ns:some-other-sa", "ns", POD_NAME, POD_UID,
                "some-other-sa", SA_UID));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("not a worker ServiceAccount"));
    }

    @Test
    void verify_psatMissingClaims_returnsInactive() {
        stubBaseActive(plainJwt(SA_SUB));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("kubernetes.io claims missing"));
    }

    @Test
    void verify_psatClaimNamespaceDiffersFromSub_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, "other-ns", POD_NAME, POD_UID, "nvcf-worker", SA_UID));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("do not match sub"));
    }

    @Test
    void verify_psatNoRegisteredIdentifiers_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        when(workerIdentifierService.findWorkerIdentifiersBySaUid(CLUSTER_ID, SA_UID))
                .thenReturn(Optional.empty());

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("no worker identifiers registered"));
    }

    @Test
    void verify_psatRegisteredNamespaceMismatch_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, "registered-elsewhere", SA_UID, POD_NAME, POD_UID));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    @Test
    void verify_psatRegisteredSaUidMismatch_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, "other-sa-uid", POD_NAME, POD_UID));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    @Test
    void verify_psatPodUidMismatch_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, "different-uid"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    @Test
    void verify_instanceOnOtherCluster_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, POD_UID));
        stubInstanceAndRequest(OTHER_CLUSTER_ID);

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("not placed on token cluster"));
        verify(instanceRequestV2Repository, never()).findRequestById(any());
    }

    @Test
    void verify_instanceMissing_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, POD_UID));
        when(instanceV2Repository.findInstanceById(INSTANCE_ID)).thenReturn(Optional.empty());

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("registered instance not found"));
    }

    @Test
    void verify_requestMissing_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, POD_UID));
        when(instanceV2Repository.findInstanceById(INSTANCE_ID))
                .thenReturn(Optional.of(instance(CLUSTER_ID)));
        when(instanceRequestV2Repository.findRequestById(REQUEST_ID)).thenReturn(Optional.empty());

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("request for instance not found"));
    }

    @Test
    void verify_requestWithoutWorkload_returnsInactive() {
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, POD_UID));
        when(instanceV2Repository.findInstanceById(INSTANCE_ID))
                .thenReturn(Optional.of(instance(CLUSTER_ID)));
        when(instanceRequestV2Repository.findRequestById(REQUEST_ID))
                .thenReturn(Optional.of(InstanceRequestV2Entity.builder()
                        .requestId(REQUEST_ID).ncaId("nca-1").build()));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("no workload binding"));
    }

    @Test
    void verify_taskRequest_returnsTaskBinding() {
        UUID taskId = UUID.randomUUID();
        stubBaseActive(psatJwt(SA_SUB, NAMESPACE, POD_NAME, POD_UID, "nvcf-worker", SA_UID));
        stubWorkerRecordBySaUid(record(SA_SUB, NAMESPACE, SA_UID, POD_NAME, POD_UID));
        when(instanceV2Repository.findInstanceById(INSTANCE_ID))
                .thenReturn(Optional.of(instance(CLUSTER_ID)));
        when(instanceRequestV2Repository.findRequestById(REQUEST_ID))
                .thenReturn(Optional.of(InstanceRequestV2Entity.builder()
                        .requestId(REQUEST_ID).ncaId("nca-1").taskId(taskId).build()));

        var outcome = service.verify("tok");

        assertTrue(outcome.isActive());
        assertEquals(taskId.toString(), outcome.getTaskId());
        assertNull(outcome.getFunctionId());
    }

    // --- SPIFFE flow ---

    @Test
    void verify_spiffeHappyPath_returnsActiveWithBinding() {
        stubBaseActive(plainJwt(SPIFFE_SUB));
        when(workerIdentifierService.findWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID))
                .thenReturn(Optional.of(record(SPIFFE_SUB, NAMESPACE, SA_UID, SPIFFE_SUB,
                        "worker-uuid-001")));
        stubInstanceAndRequest(CLUSTER_ID);

        var outcome = service.verify("tok");

        assertTrue(outcome.isActive());
        assertEquals(INSTANCE_ID, outcome.getInstanceId());
        assertEquals("worker-uuid-001", outcome.getWorkerId());
        assertEquals(WorkerTokenVerificationService.TOKEN_TYPE_SPIFFE, outcome.getTokenType());
        assertEquals(FUNCTION_ID.toString(), outcome.getFunctionId());
    }

    @Test
    void verify_spiffeSubMissingWorkerSegment_returnsInactive() {
        stubBaseActive(plainJwt("spiffe://domain/cluster/" + CLUSTER_ID + "/instance/" + INSTANCE_ID));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("/instance/{id}/worker/{wid}"));
    }

    @Test
    void verify_spiffeClusterSegmentMismatch_returnsInactive() {
        stubBaseActive(plainJwt("spiffe://domain/cluster/" + OTHER_CLUSTER_ID
                + "/instance/" + INSTANCE_ID + "/worker/w1"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("cluster does not match audience"));
        verify(workerIdentifierService, never()).findWorkerIdentifiers(any(), any());
    }

    @Test
    void verify_spiffeSubNotInRegisteredSet_returnsInactive() {
        stubBaseActive(plainJwt(SPIFFE_SUB));
        when(workerIdentifierService.findWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID))
                .thenReturn(Optional.of(record(SPIFFE_SUB, NAMESPACE, SA_UID,
                        "spiffe://other-domain/different-path", "uid")));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    @Test
    void verify_unrecognizedSubjectFormat_returnsInactive() {
        stubBaseActive(plainJwt("some-random-subject"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("unrecognized subject format"));
    }

    // --- helpers ---

    private void stubBaseActive(Jwt jwt) {
        when(nvcaTokenVerificationService.verify(eq("tok"), any()))
                .thenReturn(ClusterOIDCTokenVerificationService.Outcome.active(jwt, CLUSTER_ID));
    }

    private void stubWorkerRecordBySaUid(WorkerIdentifierRecord record) {
        when(workerIdentifierService.findWorkerIdentifiersBySaUid(CLUSTER_ID, SA_UID))
                .thenReturn(Optional.of(record));
    }

    private void stubInstanceAndRequest(String zone) {
        when(instanceV2Repository.findInstanceById(INSTANCE_ID))
                .thenReturn(Optional.of(instance(zone)));
        if (CLUSTER_ID.equals(zone)) {
            when(instanceRequestV2Repository.findRequestById(REQUEST_ID))
                    .thenReturn(Optional.of(InstanceRequestV2Entity.builder()
                            .requestId(REQUEST_ID)
                            .ncaId("nca-1")
                            .functionId(FUNCTION_ID)
                            .functionVersionId(FUNCTION_VERSION_ID)
                            .build()));
        }
    }

    private static InstanceV2Entity instance(String zone) {
        var instance = new InstanceV2Entity();
        instance.setInstanceId(INSTANCE_ID);
        instance.setRequestId(REQUEST_ID);
        instance.setZone(zone);
        return instance;
    }

    private static WorkerIdentifierRecord record(String sub, String namespace, String saUid,
            String name, String uid) {
        return WorkerIdentifierRecord.builder()
                .key(WorkerIdentifierKey.builder().clusterId(CLUSTER_ID).instanceId(INSTANCE_ID).build())
                .sub(sub)
                .namespace(namespace)
                .saUid(saUid)
                .identifiers(List.of(WorkerIdentifierUdt.builder().name(name).uid(uid).build()))
                .build();
    }

    private static Jwt psatJwt(String sub, String namespace, String podName, String podUid,
            String saName, String saUid) {
        return Jwt.withTokenValue("tok")
                .header("alg", "RS256")
                .subject(sub)
                .audience(List.of(AUDIENCE))
                .issuedAt(Instant.now())
                .expiresAt(EXP)
                .claim("kubernetes.io", Map.of(
                        "namespace", namespace,
                        "pod", Map.of("name", podName, "uid", podUid),
                        "serviceaccount", Map.of("name", saName, "uid", saUid)))
                .build();
    }

    private static Jwt plainJwt(String sub) {
        return Jwt.withTokenValue("tok")
                .header("alg", "RS256")
                .subject(sub)
                .audience(List.of(AUDIENCE))
                .issuedAt(Instant.now())
                .expiresAt(EXP)
                .build();
    }
}
