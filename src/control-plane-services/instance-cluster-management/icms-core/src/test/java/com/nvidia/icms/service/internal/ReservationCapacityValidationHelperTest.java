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
package com.nvidia.icms.service.internal;

import com.nvidia.icms.configuration.bean.InstanceTypeConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.ReservationRepository;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class ReservationCapacityValidationHelperTest {

    @Mock
    private ReservationRepository reservationRepository;

    @Mock
    private ReservationProcessor reservationProcessor;

    @Mock
    private InstanceTypeConfigurationProperties instanceTypeConfigurationProperties;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private CloudHealthRepository cloudHealthRepository;

    @InjectMocks
    private ReservationCapacityValidationHelper reservationCapacityValidationHelper;

    private UUID testReservationId;
    private ReservationEntity testReservation;
    private InstanceRequestV2Entity testInstanceRequest;
    private ClientRequestDataModel.LaunchSpecification testLaunchSpec;
    private CloudHealthEntity testCloudHealth;
    private Map<String, CloudHealthEntity> cloudHealthMap;

    @BeforeEach
    void setUp() {
        testReservationId = UUID.randomUUID();
        
        // Set up test reservation
        testReservation = ReservationEntity.builder()
                .reservationId(testReservationId)
                .ncaId("test-nca-id")
                .clusterId("test-cluster-id")
                .gpuType("H100")
                .reservedGpuCount(4)
                .availableGpuCount(2.0)
                .startTime(Instant.now().minusSeconds(3600))
                .endTime(Instant.now().plusSeconds(3600))
                .name("test-reservation")
                .lastUpdatedTime(Instant.now())
                .build();

        // Set up test instance request
        testInstanceRequest = InstanceRequestV2Entity.builder()
                .requestId("test-request-id")
                .customer("test-customer")
                .gpuCountPerInstance(2)
                .request("{\"launchSpecification\":{\"instanceType\":\"test-instance-type\"}}")
                .build();

        // Set up launch specification
        testLaunchSpec = new ClientRequestDataModel.LaunchSpecification();
        testLaunchSpec.setInstanceType("test-instance-type");

        // Set up cloud health
        testCloudHealth = new CloudHealthEntity();
        testCloudHealth.setStatus(CloudHealthStatus.UNHEALTHY);
        
        cloudHealthMap = new HashMap<>();
        cloudHealthMap.put("test-cluster-id", testCloudHealth);
    }

    @Test
    void testValidateAndGetReservationEntity_NullReservationId_ReturnsEmpty() {
        // When
        Optional<ReservationEntity> result = reservationCapacityValidationHelper.validateAndGetReservationEntity(null);

        // Then
        assertTrue(result.isEmpty());
        verify(reservationRepository, never()).findById(any());
    }

    @Test
    void testValidateAndGetReservationEntity_InvalidReservationId_ThrowsException() {
        // Given
        when(reservationRepository.findById(testReservationId)).thenReturn(Optional.empty());

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId)
        );

        assertTrue(exception.getMessage().contains("Invalid reservationId"));
        verify(reservationRepository).findById(testReservationId);
    }

    @Test
    void testValidateAndGetReservationEntity_InactiveReservation_ThrowsException() {
        // Given
        testReservation.setEndTime(Instant.now().minusSeconds(3600)); // Make reservation expired/inactive
        when(reservationRepository.findById(testReservationId)).thenReturn(Optional.of(testReservation));

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId)
        );

        assertTrue(exception.getMessage().contains("is not active"));
        verify(reservationRepository).findById(testReservationId);
    }

    @Test
    void testValidateAndGetReservationEntity_ValidActiveReservation_ReturnsReservation() {
        // Given
        when(reservationRepository.findById(testReservationId)).thenReturn(Optional.of(testReservation));

        // When
        Optional<ReservationEntity> result = reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId);

        // Then
        assertTrue(result.isPresent());
        assertEquals(testReservation, result.get());
        verify(reservationRepository).findById(testReservationId);
    }

    @Test
    void testCalculateGpuCountForInstance_GpuCountInRequest_ReturnsRequestGpuCount() {
        // Given
        testInstanceRequest.setGpuCountPerInstance(4);

        // When
        Integer result = reservationCapacityValidationHelper.calculateGpuCountForInstance(testInstanceRequest);

        // Then
        assertEquals(4, result);
        verify(instanceServiceHelper, never()).parseRequestInfo(any());
    }

    @Test
    void testCalculateGpuCountForInstance_NoGpuCountInRequest_FallbackToInstanceType() {
        // Given
        testInstanceRequest.setGpuCountPerInstance(null);
        when(instanceServiceHelper.parseRequestInfo(any())).thenReturn(
                ClientRequestDataModel.builder().launchSpecification(testLaunchSpec).build());
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("test-instance-type")).thenReturn(8);

        // When
        Integer result = reservationCapacityValidationHelper.calculateGpuCountForInstance(testInstanceRequest);

        // Then
        assertEquals(8, result);
        verify(instanceServiceHelper).parseRequestInfo(testInstanceRequest.getRequest());
        verify(instanceTypeConfigurationProperties).getGpuCountForInstanceType("test-instance-type");
    }

    @Test
    void testValidateReservationBackupCapacityForInstanceStateUpdate_Success() {
        // Given
        testInstanceRequest.setGpuCountPerInstance(2);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(4.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                testReservation, testInstanceRequest));

        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_Success() {
        // Given
        testInstanceRequest.setGpuCountPerInstance(2);
        Integer incomingInstanceCount = 1;
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(4.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                testReservation, testInstanceRequest, incomingInstanceCount));

        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservedBackupCapacity_HealthyZone_ThrowsException() {
        // Given - Zone becomes healthy
        testCloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealthMap.put("test-cluster-id", testCloudHealth);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertTrue(exception.getMessage().contains("primary zone"));
        assertTrue(exception.getMessage().contains("has become healthy"));
        verify(reservationProcessor, never()).calculateAvailableCapacityForUnhealthyZone(any());
    }

    @Test
    void testValidateReservedBackupCapacity_FullyUtilizedReservation_ThrowsException() {
        // Given
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(0.0);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertTrue(exception.getMessage().contains("fully utilized"));
        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservedBackupCapacity_InsufficientCapacity_ThrowsException() {
        // Given - Request 4 GPUs but only 2 available
        testInstanceRequest.setGpuCountPerInstance(4);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(2.0);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertTrue(exception.getMessage().contains("Insufficient reserved capacity"));
        assertTrue(exception.getMessage().contains("4"));  // Requested GPUs
        assertTrue(exception.getMessage().contains("2"));  // Available GPUs
        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_InsufficientCapacityMultipleInstances_ThrowsException() {
        // Given - Request 3 instances * 2 GPUs each = 6 GPUs, but only 4 available
        testInstanceRequest.setGpuCountPerInstance(2);
        Integer incomingInstanceCount = 3;
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(4.0);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                        testReservation, testInstanceRequest, incomingInstanceCount)
        );

        assertTrue(exception.getMessage().contains("Insufficient reserved capacity"));
        assertTrue(exception.getMessage().contains("6"));  // Requested GPUs (3 instances * 2 GPUs)
        assertTrue(exception.getMessage().contains("4"));  // Available GPUs
    }

    @Test
    void testValidateReservedBackupCapacity_ExactCapacityMatch_Success() {
        // Given - Request exactly the available capacity
        testInstanceRequest.setGpuCountPerInstance(2);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(2.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                testReservation, testInstanceRequest));

        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testCalculateGpuCountForInstance_ZeroGpuCount_FallbackToInstanceType() {
        // Given
        testInstanceRequest.setGpuCountPerInstance(0); // Zero should trigger fallback
        when(instanceServiceHelper.parseRequestInfo(any())).thenReturn(
                ClientRequestDataModel.builder().launchSpecification(testLaunchSpec).build());
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("test-instance-type")).thenReturn(1);

        // When
        Integer result = reservationCapacityValidationHelper.calculateGpuCountForInstance(testInstanceRequest);

        // Then
        assertEquals(1, result);
        verify(instanceTypeConfigurationProperties).getGpuCountForInstanceType("test-instance-type");
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_EdgeCaseScenarios() {
        // Test multiple edge cases

        // Edge Case 1: Single instance, single GPU
        testInstanceRequest.setGpuCountPerInstance(1);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(1.0);

        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                testReservation, testInstanceRequest, 1));
    }

    @Test
    void testValidateReservedBackupCapacity_CloudHealthRepositoryException_RethrowsException() {
        // Given
        when(cloudHealthRepository.findAllInMap()).thenThrow(new RuntimeException("Database error"));

        // When & Then
        RuntimeException exception = assertThrows(
                RuntimeException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertEquals("Database error", exception.getMessage());
    }

    @Test
    void testValidateReservedBackupCapacity_NonByocReservationProcessorException_RethrowsException() {
        // Given
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation))
                .thenThrow(new RuntimeException("Capacity calculation failed"));

        // When & Then
        RuntimeException exception = assertThrows(
                RuntimeException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertEquals("Capacity calculation failed", exception.getMessage());
    }

    @Test
    void testValidateAndGetReservationEntity_DatabaseException_RethrowsException() {
        // Given
        when(reservationRepository.findById(testReservationId))
                .thenThrow(new RuntimeException("Database connection failed"));

        // When & Then
        RuntimeException exception = assertThrows(
                RuntimeException.class,
                () -> reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId)
        );

        assertEquals("Database connection failed", exception.getMessage());
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_LargeNumbers() {
        // Given - Test with large numbers
        testInstanceRequest.setGpuCountPerInstance(8);
        Integer incomingInstanceCount = 100;
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(1000.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                testReservation, testInstanceRequest, incomingInstanceCount));

        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservedBackupCapacity_NegativeAvailableCapacity_ThrowsException() {
        // Given - Edge case where calculation returns negative capacity  
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(-1.0);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertTrue(exception.getMessage().contains("fully utilized"));
    }

    @Test
    void testValidateAndGetReservationEntity_ReservationAtBoundaryTime_HandlesCorrectly() {
        // Given - Reservation that just became active
        testReservation.setStartTime(Instant.now().minusSeconds(1));
        testReservation.setEndTime(Instant.now().plusSeconds(1));
        when(reservationRepository.findById(testReservationId)).thenReturn(Optional.of(testReservation));

        // When
        Optional<ReservationEntity> result = reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId);

        // Then
        assertTrue(result.isPresent());
        assertEquals(testReservation, result.get());
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_MultipleInstancesExactCapacity() {
        // Given - Request exactly matches available capacity: 2 instances * 1 GPU = 2 GPUs available
        testInstanceRequest.setGpuCountPerInstance(1);
        Integer incomingInstanceCount = 2;
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(2.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                testReservation, testInstanceRequest, incomingInstanceCount));
    }

    @Test 
    void testValidateReservedBackupCapacity_ClusterBecomesHealthy_FastFail() {
        // Given - Cluster becomes healthy during RESERVED_BACKUP flow
        testCloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealthMap.put("test-cluster-id", testCloudHealth);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertTrue(exception.getMessage().contains("primary zone"));
        assertTrue(exception.getMessage().contains("has become healthy"));
        assertTrue(exception.getMessage().contains("RESERVATION_BACKUP"));
        // Should not even check capacity since zone is healthy
        verify(reservationProcessor, never()).calculateAvailableCapacityForUnhealthyZone(any());
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_ZeroInstances_Success() {
        // Given - Edge case: zero instances requested
        testInstanceRequest.setGpuCountPerInstance(1);
        Integer incomingInstanceCount = 0;
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(1.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                testReservation, testInstanceRequest, incomingInstanceCount));

        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservationBackupCapacityForInstanceStateUpdate_WithFallbackGpuCount() {
        // Given - No GPU count in request, should use fallback
        testInstanceRequest.setGpuCountPerInstance(null);
        when(instanceServiceHelper.parseRequestInfo(any())).thenReturn(
                ClientRequestDataModel.builder().launchSpecification(testLaunchSpec).build());
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("test-instance-type")).thenReturn(4);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(8.0);

        // When & Then
        assertDoesNotThrow(() -> 
            reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                testReservation, testInstanceRequest));

        verify(instanceServiceHelper).parseRequestInfo(testInstanceRequest.getRequest());
        verify(instanceTypeConfigurationProperties).getGpuCountForInstanceType("test-instance-type");
        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservedBackupCapacity_UnexpectedException_RethrowsException() {
        // Given - Unexpected exception during capacity calculation
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation))
                .thenThrow(new RuntimeException("Unexpected error"));

        // When & Then
        RuntimeException exception = assertThrows(
                RuntimeException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertEquals("Unexpected error", exception.getMessage());
        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateReservedBackupCapacity_BackupDisabled_ThrowsExceptionBeforeHealthLookup() {
        // Given - reservation opted out of backup while the request was already in flight
        testReservation.setReservationBackUpDisabled(true);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        testReservation, testInstanceRequest)
        );

        assertTrue(exception.getMessage().contains("has backup disabled"));
        assertTrue(exception.getMessage().contains(testReservationId.toString()));
        // Fast fails before any zone health or capacity lookup
        verify(cloudHealthRepository, never()).findAllInMap();
        verify(reservationProcessor, never()).calculateAvailableCapacityForUnhealthyZone(any());
    }

    @Test
    void testValidateReservationBackupCapacityForRequestStateUpdate_BackupDisabled_ThrowsException() {
        // Given
        testReservation.setReservationBackUpDisabled(true);

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(
                        testReservation, testInstanceRequest, 1)
        );

        assertTrue(exception.getMessage().contains("has backup disabled"));
        verify(cloudHealthRepository, never()).findAllInMap();
        verify(reservationProcessor, never()).calculateAvailableCapacityForUnhealthyZone(any());
    }

    @Test
    void testValidateReservedBackupCapacity_BackupExplicitlyEnabled_Success() {
        // Given - explicit false must behave exactly like the legacy null default
        testReservation.setReservationBackUpDisabled(false);
        testInstanceRequest.setGpuCountPerInstance(2);
        when(cloudHealthRepository.findAllInMap()).thenReturn(cloudHealthMap);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(testReservation)).thenReturn(4.0);

        // When & Then
        assertDoesNotThrow(() ->
            reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                testReservation, testInstanceRequest));

        verify(reservationProcessor).calculateAvailableCapacityForUnhealthyZone(testReservation);
    }

    @Test
    void testValidateAndGetReservationEntity_ReservationJustExpired_ThrowsException() {
        // Given - Reservation that just expired
        testReservation.setEndTime(Instant.now().minusSeconds(1)); // Just expired
        when(reservationRepository.findById(testReservationId)).thenReturn(Optional.of(testReservation));

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId)
        );

        assertTrue(exception.getMessage().contains("is not active"));
        verify(reservationRepository).findById(testReservationId);
    }
}
