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
package com.nvidia.icms.outbound.cassandra.instance;

import static com.nvidia.icms.factory.InstanceEntityFactory.createDefaultInstanceV2;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.DbQueryExecutorService;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;

@Slf4j
@ExtendWith(MockitoExtension.class)
class InstanceV2RepositoryTest extends InstanceTestBase {

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    private InstanceV2Repository instanceV2Repository;

    @Autowired
    private DbConfigurationProperties dbConfigurationProperties;

    @Autowired
    private InstanceV2Repo instanceV2Repo;

    @Autowired
    private InstanceByDayRepo instanceByDayRepo;

    private InstancePerZoneRepository instancePerZoneRepository;

    @Autowired
    private TelemetryEventClient telemetryEventClient;

    @Autowired
    private InstanceByZoneRepo instanceByZoneRepo;

    @Autowired
    private LockProviderService lockProviderService;

    @Autowired
    private ByocTerminateService byocTerminateService;

    @Autowired
    private AppAuditService auditService;

    @Autowired
    private AwsConfigurationProperties awsConfigurationProperties;

    @Autowired
    private InstanceServiceHelper instanceServiceHelper;

    @Autowired
    private DbQueryExecutorService dbQueryExecutorService;

    @BeforeEach
    void setup() {
        instancePerZoneRepository = new InstancePerZoneRepository(instanceByZoneRepo,
                                                                          instanceV2Repo,
                                                                          dbConfigurationProperties,
                                                                          icmsConfigurationProperties);

        instanceV2Repository = new InstanceV2Repository(instanceV2Repo,
                                                                instanceByDayRepo,
                                                                instancePerZoneRepository,
                                                                icmsConfigurationProperties,
                                                                telemetryEventClient,
                                                                dbQueryExecutorService);
    }

    @ParameterizedTest
    @ValueSource(ints = {1, 0, -1})
    void insert_ok(int monthOffset) {

        //arrange
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);
        String requestId = RandomFactory.getRandomStringWithPrefix("requestid", 5);
        Instant createdDate = TimeUtils.getSameDateTimeOfPreviousMonth(monthOffset);

        InstanceV2Entity newEntity = createDefaultInstanceV2(instanceId,
                                                                                               requestId,
                                                                                               createdDate,
                                                                                               customer,
                                                                                               null);

        //act
        instanceV2Repository.insert(newEntity);

        Optional<InstanceV2Entity> instanceV2Entity = instanceV2Repo.findById(instanceId);
        Optional<InstanceByDayEntity> instanceByDayEntity = instanceByDayRepo.findByKeyTruncatedTsByDayAndKeyInstanceId(createdDate.truncatedTo(ChronoUnit.DAYS), instanceId);
        Optional<InstanceByZoneEntity> instanceByZoneEntity = instancePerZoneRepository.findByZoneAndInstanceId(newEntity.getZone(), instanceId);

        //assert

        assertInstanceV2Entity(instanceV2Entity, instanceId, requestId, customer, createdDate);
        assertInstanceByDayEntity(instanceByDayEntity, instanceId, requestId, createdDate);
        assertInstanceByZoneEntity(instanceByZoneEntity, instanceId, requestId, newEntity.getZone());

        assertEquals(SpotInstanceInternalState.RUNNING, instanceV2Entity.get().getInstanceStateName());
        assertEquals(SpotInstanceRequestState.ACTIVE, instanceV2Entity.get().getRequestState());
        assertEquals(SpotInstanceStatus.FULFILLED, instanceV2Entity.get().getRequestStatusCode());

    }


    @ParameterizedTest
    @ValueSource(ints = {1, 0, -1})
    void delete_ok(int monthOffset) {

        //Arrange
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);
        String requestId = RandomFactory.getRandomStringWithPrefix("requestid", 5);
        Instant createdDate = TimeUtils.getSameDateTimeOfPreviousMonth(monthOffset);

        InstanceV2Entity newEntity = createDefaultInstanceV2(instanceId,
                                                                     requestId,
                                                                     createdDate,
                                                                     customer,
                                                                     null);

        instanceV2Repository.insert(newEntity);

        // Act
        instanceV2Repository.delete(newEntity, true);

        Optional<InstanceV2Entity> instanceV2Entity = instanceV2Repository.findInstanceById(instanceId);
        Optional<InstanceByDayEntity> instanceByDayEntity = instanceByDayRepo.findByKeyTruncatedTsByDayAndKeyInstanceId(createdDate.truncatedTo(ChronoUnit.DAYS), instanceId);

        // Assert
        assertTrue(instanceV2Entity.isEmpty());
        assertTrue(instanceByDayEntity.isPresent());
        assertTrue(instanceByDayEntity.get().isMarkedAsDeleted());

    }


    @ParameterizedTest
    @ValueSource(ints = {1, 0, -1})
    void restore_ok(int monthOffset) {

        //arrange
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);
        String requestId = RandomFactory.getRandomStringWithPrefix("requestid", 5);
        Instant createdDate = TimeUtils.getSameDateTimeOfPreviousMonth(monthOffset);

        InstanceV2Entity newEntity = createDefaultInstanceV2(instanceId,
                                                                     requestId,
                                                                     createdDate,
                                                                     customer,
                                                                     null);

        //act
        instanceV2Repository.restore(newEntity);

        Optional<InstanceV2Entity> instanceV2Entity = instanceV2Repository.findInstanceById(instanceId);
        Optional<InstanceByDayEntity> instanceByDayEntity = instanceByDayRepo.findByKeyTruncatedTsByDayAndKeyInstanceId(createdDate.truncatedTo(ChronoUnit.DAYS), instanceId);

        //assert
        assertInstanceV2Entity(instanceV2Entity, instanceId, requestId, customer, createdDate);
        assertInstanceByDayEntity(instanceByDayEntity, instanceId, requestId, createdDate);
    }

    //---------------------------------------------------------------------------------

    @Test
    void findByCustomerAndInstanceId_found() {

        //arrange
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);

        InstanceV2Entity newEntity = createDefaultInstanceV2(instanceId,
                                                                     null,
                                                                     null,
                                                                     customer,
                                                                     null);
        instanceV2Repository.insert(newEntity);

        //act
        Optional<InstanceV2Entity> instanceV2Entity =
                instanceV2Repository.findInstanceByCustomerAndId(customer, instanceId);

        //assert
        assertInstanceV2Entity(instanceV2Entity, instanceId, null, customer, null);
    }

    @Test
    void findByCustomerAndInstanceId_wrong_customer_notFound() {

        //arrange
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);

        InstanceV2Entity newEntity = createDefaultInstanceV2(instanceId,
                                                                     null,
                                                                     null,
                                                                     null, // random customer id
                                                                     null);
        instanceV2Repository.insert(newEntity);

        //act
        IcmsBadRequestException exception =
                Assertions.assertThrowsExactly(IcmsBadRequestException.class, () ->
                        instanceV2Repository.findInstanceByCustomerAndId(customer, instanceId));

        //assert
        assertEquals(
                String.format("Could not find instance with %s id", instanceId),
                exception.getBody().getDetail());
    }

    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findByCustomerAndInstanceId_customer_not_provided_ignored(boolean isCustomerNull) {
        //arrange
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);

        InstanceV2Entity newEntity = createDefaultInstanceV2(instanceId,
                                                                     null,
                                                                     null,
                                                                     null, // random customer id
                                                                     null);
        instanceV2Repository.insert(newEntity);

        //act
        IcmsBadRequestException exception =
                Assertions.assertThrowsExactly(IcmsBadRequestException.class, () ->
                        instanceV2Repository.findInstanceByCustomerAndId(isCustomerNull ? null : "", instanceId));

        //assert
        assertEquals(
                String.format("Could not find instance with %s id", instanceId),
                exception.getBody().getDetail());
    }

    @Test
    void findByCustomerAndInstanceId_wrongId_notFound() {
        //arrange
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);
        String instanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);

        InstanceV2Entity newEntity = createDefaultInstanceV2(null, // random id
                                                                     null,
                                                                     null,
                                                                     customer,
                                                                     null);
        instanceV2Repository.insert(newEntity);

        //act
        Optional<InstanceV2Entity> instanceV2Entity =
                instanceV2Repository.findInstanceByCustomerAndId(customer, instanceId);

        //assert
        assertTrue(instanceV2Entity.isEmpty());
    }

    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findByCustomerAndInstanceId_empty_parameters(boolean isNull) {
        //arrange
        InstanceV2Entity newEntity = createDefaultInstanceV2(null, // random id
                                                                     null,
                                                                     null,
                                                                     null,
                                                                     null);
        instanceV2Repository.insert(newEntity);

        //act
        Optional<InstanceV2Entity> instanceV2Entity =
                instanceV2Repository.findInstanceByCustomerAndId(isNull ? null : "", isNull ? null : "");

        //assert
        assertTrue(instanceV2Entity.isEmpty());
    }

    @Test
    void findInstancesByIds_ok() {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(2, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                     null,
                                                                     null,
                                                                     null,
                                                                     null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(null, // random ID
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByIds(new HashSet<>(instanceIds));

        //assert
        assertEquals(2, entities.size());
        assertInstancePresents(entities, instanceIds.get(0));
        assertInstancePresents(entities, instanceIds.get(1));
    }

    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findInstancesByIds_empty_parameter(boolean isNull) {
        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(2, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(null, // random ID
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByIds(isNull ? null : new HashSet<>());

        //assert
        assertTrue(entities.isEmpty());
    }


    @Test
    void findInstancesByCustomerAndIds_ok() {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    null,
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    null,
                                                                    null,
                                                                    customer,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByCustomerAndIds(customer, new HashSet<>(instanceIds));

        //assert
        assertEquals(2, entities.size());
        assertInstancePresents(entities, instanceIds.get(1));
        assertInstancePresents(entities, instanceIds.get(2));
    }

    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findInstancesByCustomerAndIds_no_customer_ok(boolean isCustomerNull) {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    null,
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    null,
                                                                    null,
                                                                    customer,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByCustomerAndIds(isCustomerNull ? null : "", new HashSet<>(instanceIds));

        //assert
        assertEquals(3, entities.size()); // all instances should be found
        assertInstancePresents(entities, instanceIds.get(0));
        assertInstancePresents(entities, instanceIds.get(1));
        assertInstancePresents(entities, instanceIds.get(2));
    }

    @Test
    void findInstancesByCustomerAndIds_empty_ids() {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    null,
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    null,
                                                                    null,
                                                                    customer,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByCustomerAndIds(customer, new HashSet<>());

        //assert
        assertTrue(entities.isEmpty());
    }

    @Test
    void findInstancesByRequestId_ok() {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(3, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(2, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByRequestId(requestIds.get(1));

        //assert
        assertEquals(2, entities.size());
        assertInstancePresents(entities, instanceIds.get(1));
        assertInstancePresents(entities, instanceIds.get(2));
    }

    @Test
    void findInstancesByGpuSpecificationId_ok() {
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecificationId = UUID.randomUUID();
        UUID differentGpuSpecificationId = UUID.randomUUID();
        String matchingInstanceId = RandomFactory.getRandomStringWithPrefix("instanceid", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(
                matchingInstanceId,
                null,
                null,
                null,
                entity -> {
                    entity.setDeploymentId(deploymentId);
                    entity.setGpuSpecificationId(gpuSpecificationId);
                }));
        instanceV2Repository.insert(createDefaultInstanceV2(
                RandomFactory.getRandomStringWithPrefix("instanceid", 5),
                null,
                null,
                null,
                entity -> {
                    entity.setDeploymentId(deploymentId);
                    entity.setGpuSpecificationId(differentGpuSpecificationId);
                }));
        instanceV2Repository.insert(createDefaultInstanceV2(
                RandomFactory.getRandomStringWithPrefix("instanceid", 5),
                null,
                null,
                null,
                entity -> {
                    entity.setDeploymentId(UUID.randomUUID());
                    entity.setGpuSpecificationId(gpuSpecificationId);
                }));

        List<InstanceV2Entity> byDeployment =
                instanceV2Repository.findInstancesByDeploymentId(deploymentId);
        List<InstanceV2Entity> byDeploymentAndGpu =
                instanceV2Repository.findInstancesByGpuSpecificationId(
                        deploymentId,
                        gpuSpecificationId);

        assertEquals(2, byDeployment.size());
        assertEquals(1, byDeploymentAndGpu.size());
        assertEquals(matchingInstanceId, byDeploymentAndGpu.getFirst().getInstanceId());
    }

    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findInstancesByRequestId_empty_id(boolean isNull) {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(3, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(2, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByRequestId(isNull ? null : "");

        //assert
        assertTrue(entities.isEmpty());
    }

    @Test
    void findInstancesByRequestIds_ok() {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    null,
                                                                    null));

        Set<String> lookupIds = new HashSet<>();
        lookupIds.add(requestIds.get(0));
        lookupIds.add(requestIds.get(2));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByRequestIds(lookupIds);

        //assert
        assertEquals(3, entities.size());
        assertInstancePresents(entities, instanceIds.get(0));
        assertInstancePresents(entities, instanceIds.get(1));
        assertInstancePresents(entities, instanceIds.get(3));
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findInstancesByRequestIds_empty_ids(boolean isNull) {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByRequestIds(isNull ? null : new HashSet<>());

        //assert
        assertTrue(entities.isEmpty());
    }


    @Test
    void findInstancesByCustomerAndRequestIds_ok() {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    null,
                                                                    null));

        Set<String> lookupIds = new HashSet<>();
        lookupIds.add(requestIds.get(0));
        lookupIds.add(requestIds.get(2));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByCustomerAndRequestIds(customer, lookupIds);

        //assert
        assertEquals(1, entities.size());
        assertInstancePresents(entities, instanceIds.get(0));
    }


    @Test
    void findInstancesByCustomerAndRequestIds_wrong_customer_ok() {
        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    null,
                                                                    null));

        Set<String> lookupIds = new HashSet<>();
        lookupIds.add(requestIds.get(0));
        lookupIds.add(requestIds.get(2));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByCustomerAndRequestIds(customer, lookupIds);

        //assert
        assertTrue(entities.isEmpty());
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findInstancesByCustomerAndRequestIds_empty_parameters(boolean isNull) {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesByCustomerAndRequestIds(isNull ? null : "",  new HashSet<>());

        //assert
        assertTrue(entities.isEmpty());
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findAllInstancesByCustomerAndRequestIds_ok(boolean inParallel) {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);



        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    customer,
                                                                    null));

        Set<String> lookupIds = new HashSet<>();
        lookupIds.add(requestIds.get(0));
        lookupIds.add(requestIds.get(2));

        if (inParallel) {
            Mockito.when(icmsConfigurationProperties.getFindInstanceByRequestIdCallsPerThread())
                    .thenReturn(1);
            Mockito.when(icmsConfigurationProperties.getFindInstanceByRequestIdThreadsInParallel())
                    .thenReturn(1);
        }

        //act
        Map<String, List<InstanceV2Entity>> entities = instanceV2Repository.findAllInstancesByCustomerAndRequestIds(customer, lookupIds, inParallel);

        //assert
        assertEquals(2, entities.keySet().size());
        assertEquals(1, entities.get(requestIds.get(0)).size());
        assertEquals(1, entities.get(requestIds.get(2)).size());

        assertInstancePresents(entities.get(requestIds.get(0)), instanceIds.get(0));
        assertInstancePresents(entities.get(requestIds.get(2)), instanceIds.get(3));
    }


    @Test
    void findAllInstancesByCustomerAndRequestIds_wrong_customer_ok() {
        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    customer,
                                                                    null));

        Set<String> lookupIds = new HashSet<>();
        lookupIds.add(requestIds.get(0));
        lookupIds.add(requestIds.get(2));

        //act
        Map<String, List<InstanceV2Entity>> entities =
                instanceV2Repository.findAllInstancesByCustomerAndRequestIds(
                        RandomFactory.getRandomStringWithPrefix("customer", 5),
                        lookupIds, false);

        //assert
        assertEquals(2, entities.keySet().size());
        assertEquals(0, entities.get(requestIds.get(0)).size());
        assertEquals(0, entities.get(requestIds.get(2)).size());
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findAllInstancesByCustomerAndRequestIds_empty_parameters(boolean isNull) {

        //arrange
        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");
        List<String> requestIds = RandomFactory.getRandomStringList(3, "instanceId");
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestIds.get(0),
                                                                    null,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestIds.get(1),
                                                                    null,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestIds.get(2),
                                                                    null,
                                                                    customer,
                                                                    null));

        //act
        Map<String, List<InstanceV2Entity>> entities =
                instanceV2Repository.findAllInstancesByCustomerAndRequestIds(
                        isNull ? null : "",
                        isNull ? null : new HashSet<>(),
                        false);

        //assert
        assertTrue(entities.isEmpty());
    }


    @Test
    void findInstanceIdsPerPeriod_ok() {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateYesterday = createDateNow.minus(1, ChronoUnit.DAYS);
        Instant createDateYesterday2 = createDateNow.minus(2, ChronoUnit.DAYS);
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateYesterday,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateYesterday2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));

        //act
        List<String> entityIds = instanceV2Repository.findInstanceIdsPerPeriod(createDateLastMonth, createDateYesterday2);

        //assert
        assertEquals(2, entityIds.size());
        assertTrue( entityIds.contains(instanceIds.get(2)));
        assertTrue( entityIds.contains(instanceIds.get(3)));
    }

    @Test
    void findInstanceIdsPerPeriod_wrong_time_ok() {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateYesterday = createDateNow.minus(1, ChronoUnit.DAYS);
        Instant createDateYesterday2 = createDateNow.minus(2, ChronoUnit.DAYS);
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateYesterday,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateYesterday2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));

        Instant lookUpStart = TimeUtils.getSameDateTimeOfPreviousMonth(3);
        Instant lookUpEnd = TimeUtils.getSameDateTimeOfPreviousMonth(2);

        //act
        List<String> entityIds = instanceV2Repository.findInstanceIdsPerPeriod(lookUpStart, lookUpEnd);

        //assert
        assertTrue( entityIds.isEmpty());
    }

    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void findInstanceIdsPerPeriod_empty_params(boolean fromNull, boolean toNull) {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateYesterday = createDateNow.minus(1, ChronoUnit.DAYS);
        Instant createDateYesterday2 = createDateNow.minus(2, ChronoUnit.DAYS);
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateYesterday,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateYesterday2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));

        //act
        List<String> entityIds = instanceV2Repository.findInstanceIdsPerPeriod(fromNull ? null : createDateYesterday2, toNull ? null : createDateLastMonth);

        //assert
        assertTrue( entityIds.isEmpty());
    }

    @Test
    void findInstancesPerPeriod_ok() {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateYesterday = createDateNow.minus(1, ChronoUnit.DAYS);
        Instant createDateYesterday2 = createDateNow.minus(2, ChronoUnit.DAYS);
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateYesterday,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateYesterday2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesPerPeriod(createDateLastMonth, createDateYesterday2);

        //assert
        assertEquals(2, entities.size());
        assertInstancePresents(entities, instanceIds.get(2));
        assertInstancePresents(entities, instanceIds.get(3));
    }

    @Test
    void findInstancesPerPeriod_wrong_time_ok() {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateYesterday = createDateNow.minus(1, ChronoUnit.DAYS);
        Instant createDateYesterday2 = createDateNow.minus(2, ChronoUnit.DAYS);
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateYesterday,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateYesterday2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));

        Instant lookUpStart = TimeUtils.getSameDateTimeOfPreviousMonth(3);
        Instant lookUpEnd = TimeUtils.getSameDateTimeOfPreviousMonth(2);

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesPerPeriod(lookUpStart, lookUpEnd);

        //assert
        assertTrue( entities.isEmpty());
    }

    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void findInstancesPerPeriod_empty_params(boolean fromNull, boolean toNull) {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateYesterday = createDateNow.minus(1, ChronoUnit.DAYS);
        Instant createDateYesterday2 = createDateNow.minus(2, ChronoUnit.DAYS);
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateYesterday,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateYesterday2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findInstancesPerPeriod(fromNull ? null : createDateYesterday2, toNull ? null : createDateLastMonth);

        //assert
        assertTrue( entities.isEmpty());
    }

    @Test
    void findAllForPreviousMonths_ok() {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);
        Instant createDateLastMonth2 = TimeUtils.getSameDateTimeOfPreviousMonth(2);
        Instant createDateLastMonth3 = TimeUtils.getSameDateTimeOfPreviousMonth(3);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateLastMonth2,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth3,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findAllForPreviousMonths(2);

        //assert
        assertEquals(3, entities.size());
        assertInstancePresents(entities, instanceIds.get(0));
        assertInstancePresents(entities, instanceIds.get(1));
        assertInstancePresents(entities, instanceIds.get(2));
    }

    @Test
    void findAllByCustomer_ok() {

        //arrange
        Instant createDateNow = Instant.now();
        Instant createDateLastMonth = TimeUtils.getSameDateTimeOfPreviousMonth(1);
        Instant createDateLastMonth2 = TimeUtils.getSameDateTimeOfPreviousMonth(2);
        Instant createDateLastMonth3 = TimeUtils.getSameDateTimeOfPreviousMonth(3);

        String requestId = RandomFactory.getRandomStringWithPrefix("requestId", 5);
        String customer = RandomFactory.getRandomStringWithPrefix("customer", 5);

        List<String> instanceIds = RandomFactory.getRandomStringList(4, "instanceId");

        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(0),
                                                                    requestId,
                                                                    createDateNow,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(1),
                                                                    requestId,
                                                                    createDateLastMonth,
                                                                    null,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(2),
                                                                    requestId,
                                                                    createDateLastMonth2,
                                                                    customer,
                                                                    null));
        instanceV2Repository.insert(createDefaultInstanceV2(instanceIds.get(3),
                                                                    requestId,
                                                                    createDateLastMonth3,
                                                                    null,
                                                                    null));

        //act
        List<InstanceV2Entity> entities = instanceV2Repository.findAllByCustomer(customer);

        //assert
        assertEquals(2, entities.size());
        assertInstancePresents(entities, instanceIds.get(0));
        assertInstancePresents(entities, instanceIds.get(2));
    }

}
