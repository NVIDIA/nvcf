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
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierUdt;
import com.nvidia.icms.service.byoc.nvca.NvcaTokenVerificationService;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class WorkerTokenVerificationServiceTest {

    private static final String CLUSTER_ID = "cl-abc123";
    private static final String INSTANCE_ID = "inst-789";
    private static final String POD_NAME = "nvcf-worker-inst-789-0";
    private static final String POD_UID = "f1e2d3c4-5b6a-7980-1234-aabbccddeeff";
    private static final String SA_SUB = "system:serviceaccount:nvcf-backend:nvcf-worker-" + INSTANCE_ID;
    private static final String AUDIENCE = "nvcf-icms:" + CLUSTER_ID;
    private static final String SPIFFE_SUB =
            "spiffe://domain/cluster/c1/instance/" + INSTANCE_ID + "/worker/worker-uuid-001";

    @Mock
    private NvcaTokenVerificationService nvcaTokenVerificationService;

    @Mock
    private WorkerIdentifierService workerIdentifierService;

    private WorkerTokenVerificationService service;

    @BeforeEach
    void setUp() {
        service = new WorkerTokenVerificationService(nvcaTokenVerificationService, workerIdentifierService);
    }

    // --- base verification delegation ---

    @Test
    void verify_baseRejectsToken_propagatesRejection() {
        when(nvcaTokenVerificationService.verify("tok"))
                .thenReturn(NvcaTokenVerificationService.Outcome.reject(
                        NvcaTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                        "JWT verification failed"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertEquals("JWT verification failed", outcome.getErrorMessage());
        verify(workerIdentifierService, never()).findWorkerIdentifiers(any(), any());
    }

    @Test
    void verify_unknownCluster_propagatesUnknownClusterReason() {
        when(nvcaTokenVerificationService.verify("tok"))
                .thenReturn(NvcaTokenVerificationService.Outcome.reject(
                        NvcaTokenVerificationService.RejectReason.UNKNOWN_CLUSTER,
                        "No cluster found"));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertEquals(NvcaTokenVerificationService.RejectReason.UNKNOWN_CLUSTER, outcome.getReason());
    }

    // --- PSAT (SAT) flow ---

    @Test
    void verify_psatHappyPath_returnsActiveWithCorrectFields() {
        Jwt jwt = psatJwt(SA_SUB, POD_NAME, POD_UID);
        stubBaseActive(jwt);
        stubWorkerRecord(SA_SUB, List.of(WorkerIdentifierUdt.builder()
                .name(POD_NAME).uid(POD_UID).build()));

        var outcome = service.verify("tok");

        assertTrue(outcome.isActive());
        assertEquals(CLUSTER_ID, outcome.getClusterId());
        assertEquals(INSTANCE_ID, outcome.getInstanceId());
        assertEquals(POD_NAME, outcome.getWorkerId());
        assertEquals(WorkerTokenVerificationService.TOKEN_TYPE_PSAT, outcome.getTokenType());
    }

    @Test
    void verify_psatSubNoNvcfWorkerPrefix_returnsInactive() {
        Jwt jwt = psatJwt("system:serviceaccount:ns:some-other-sa", POD_NAME, POD_UID);
        stubBaseActive(jwt);

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker SA pattern"));
    }

    @Test
    void verify_psatSubMalformed_returnsInactive() {
        Jwt jwt = plainJwt("not-a-serviceaccount-sub");
        stubBaseActive(jwt);

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
    }

    @Test
    void verify_psatMissingPodClaims_returnsInactive() {
        Jwt jwt = Jwt.withTokenValue("tok")
                .header("alg", "RS256")
                .subject(SA_SUB)
                .audience(List.of(AUDIENCE))
                .issuedAt(Instant.now())
                .expiresAt(Instant.now().plusSeconds(900))
                .build();
        stubBaseActive(jwt);

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("kubernetes.io pod claims missing"));
    }

    @Test
    void verify_psatNoRegisteredIdentifiers_returnsInactive() {
        Jwt jwt = psatJwt(SA_SUB, POD_NAME, POD_UID);
        stubBaseActive(jwt);
        when(workerIdentifierService.findWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID))
                .thenReturn(Optional.empty());

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("no worker identifiers registered"));
    }

    @Test
    void verify_psatSubMismatch_returnsInactive() {
        Jwt jwt = psatJwt(SA_SUB, POD_NAME, POD_UID);
        stubBaseActive(jwt);
        stubWorkerRecord("system:serviceaccount:nvcf-backend:nvcf-worker-other-id",
                List.of(WorkerIdentifierUdt.builder().name(POD_NAME).uid(POD_UID).build()));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    @Test
    void verify_psatPodUidMismatch_returnsInactive() {
        Jwt jwt = psatJwt(SA_SUB, POD_NAME, POD_UID);
        stubBaseActive(jwt);
        stubWorkerRecord(SA_SUB, List.of(WorkerIdentifierUdt.builder()
                .name(POD_NAME).uid("different-uid").build()));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    // --- SPIFFE flow ---

    @Test
    void verify_spiffeHappyPath_returnsActiveWithCorrectFields() {
        Jwt jwt = plainJwt(SPIFFE_SUB);
        stubBaseActive(jwt);
        stubWorkerRecord(SPIFFE_SUB, List.of(WorkerIdentifierUdt.builder()
                .name(SPIFFE_SUB).uid("worker-uuid-001").build()));

        var outcome = service.verify("tok");

        assertTrue(outcome.isActive());
        assertEquals(INSTANCE_ID, outcome.getInstanceId());
        assertEquals("worker-uuid-001", outcome.getWorkerId());
        assertEquals(WorkerTokenVerificationService.TOKEN_TYPE_SPIFFE, outcome.getTokenType());
    }

    @Test
    void verify_spiffeSubMissingWorkerSegment_returnsInactive() {
        String badSpiffe = "spiffe://domain/cluster/c1/instance/" + INSTANCE_ID;
        Jwt jwt = plainJwt(badSpiffe);
        stubBaseActive(jwt);

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("/instance/{id}/worker/{wid}"));
    }

    @Test
    void verify_spiffeSubNotInRegisteredSet_returnsInactive() {
        Jwt jwt = plainJwt(SPIFFE_SUB);
        stubBaseActive(jwt);
        stubWorkerRecord(SPIFFE_SUB, List.of(WorkerIdentifierUdt.builder()
                .name("spiffe://other-domain/different-path").uid("uid").build()));

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("worker identity not in registered set"));
    }

    @Test
    void verify_unrecognizedSubjectFormat_returnsInactive() {
        Jwt jwt = plainJwt("some-random-subject");
        stubBaseActive(jwt);

        var outcome = service.verify("tok");

        assertFalse(outcome.isActive());
        assertTrue(outcome.getErrorMessage().contains("unrecognized subject format"));
    }

    // --- helpers ---

    private void stubBaseActive(Jwt jwt) {
        when(nvcaTokenVerificationService.verify("tok"))
                .thenReturn(NvcaTokenVerificationService.Outcome.active(jwt, CLUSTER_ID));
    }

    private void stubWorkerRecord(String sub, List<WorkerIdentifierUdt> identifiers) {
        var record = WorkerIdentifierRecord.builder()
                .sub(sub)
                .identifiers(identifiers)
                .build();
        when(workerIdentifierService.findWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID))
                .thenReturn(Optional.of(record));
    }

    private static Jwt psatJwt(String sub, String podName, String podUid) {
        return Jwt.withTokenValue("tok")
                .header("alg", "RS256")
                .subject(sub)
                .audience(List.of(AUDIENCE))
                .issuedAt(Instant.now())
                .expiresAt(Instant.now().plusSeconds(900))
                .claim("kubernetes.io", Map.of(
                        "namespace", "nvcf-backend",
                        "pod", Map.of("name", podName, "uid", podUid),
                        "serviceaccount", Map.of("name", "nvcf-worker-" + INSTANCE_ID, "uid", "sa-uid")))
                .build();
    }

    private static Jwt plainJwt(String sub) {
        return Jwt.withTokenValue("tok")
                .header("alg", "RS256")
                .subject(sub)
                .audience(List.of(AUDIENCE))
                .issuedAt(Instant.now())
                .expiresAt(Instant.now().plusSeconds(900))
                .build();
    }
}
