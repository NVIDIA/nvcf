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
package com.nvidia.icms.outbound.cassandra.request;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.datastax.oss.driver.api.core.cql.SyncCqlSession;
import com.google.common.base.Stopwatch;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.factory.InstanceRequestEntityFactory;
import com.nvidia.icms.factory.UpdateEntity;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayKey;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.TerminateInstanceService;
import com.nvidia.icms.service.scheduled.cleanup.DatabaseCleanupTask;
import com.nvidia.icms.service.scheduled.cleanup.RequestsByDayCleanupExecutor;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.DbQuery;
import com.nvidia.icms.util.DbQueryExecutor;
import com.nvidia.icms.util.DbQueryExecutorService;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Random;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import jakarta.annotation.Nullable;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.MockitoAnnotations;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.data.domain.Limit;


public class InstanceRequestV2RepositoryTest  extends InstanceRequestTestBase {

    @Autowired
    InstanceRequestV2Repo instanceRequestV2Repo;
    @Autowired
    InstanceRequestV2ByDayRepo instanceRequestV2ByDayRepo;
    @Autowired
    TerminateInstanceService terminateInstanceService;

    @Mock
    TelemetryEventClient telemetryEventClient;
    @Mock
    IcmsConfigurationProperties icmsConfigurationProperties;

    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Autowired
    private DbQueryExecutorService dbQueryExecutorService;

    @Autowired
    private LockProviderService lockProviderService;

    @BeforeEach
    void init() {
        MockitoAnnotations.openMocks(this);
        instanceRequestV2Repository = new InstanceRequestV2Repository(instanceRequestV2Repo,
                                                              instanceRequestV2ByDayRepo,
                                                              telemetryEventClient,
                                                              icmsConfigurationProperties);
    }

    @ParameterizedTest
    @ValueSource(ints = {1, 0, -1})
    void insert(int monthOffset) {

        //Arrange
        String requestId = "requestID";
        Instant createTime = TimeUtils.getSameDateTimeOfPreviousMonth(monthOffset);
        String customer = "cust";
        String statusMessage = "Instance request status set to fulfilled";

        // act
        InstanceRequestV2Entity inserted = insertRequestsInDb(requestId, 1, createTime, customer,
                                                          (r) -> {
                                                              r.setState(
                                                                      SpotInstanceRequestState.ACTIVE);
                                                              r.setStatusCode(
                                                                      SpotRequestStatusCode.FULFILLED.toString());
                                                              r.setStatusMessage(statusMessage);
                                                          }).getFirst();

        Optional<InstanceRequestV2Entity> instanceRequestV2Entity = instanceRequestV2Repository.instanceRequestV2Repo.findById(
                requestId);
        Optional<InstanceRequestV2ByDayEntity> requestByDay = findInstanceRequestV2ByDayEntity(
                createTime.truncatedTo(ChronoUnit.DAYS),
                requestId);


        //assert
        assertInstanceRequestV2Entity(instanceRequestV2Entity, requestId, customer, createTime);
        assertInstanceRequestV2EntityEquals(inserted, instanceRequestV2Entity.get());

        assertInstanceRequestV2ByDayEntity(requestByDay, requestId, createTime);
        assertEquals(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES,
                     instanceRequestV2Entity.get().getAction());
        assertEquals(SpotInstanceRequestState.ACTIVE, instanceRequestV2Entity.get().getState());

        assertEquals(SpotRequestStatusCode.FULFILLED.toString(),
                     instanceRequestV2Entity.get().getStatusCode());
        assertEquals(statusMessage, instanceRequestV2Entity.get().getStatusMessage());

    }


    @ParameterizedTest
    @ValueSource(ints = {1, 0, -1})
    void delete(int monthOffset) {

        //Arrange
        String requestId = "requestID";
        Instant createTime = TimeUtils.getSameDateTimeOfPreviousMonth(monthOffset);
        String customer = "cust";

        InstanceRequestV2Entity inserted = insertRequestsInDb(requestId, 1, createTime, customer, null).getFirst();
        Optional<InstanceRequestV2Entity> instanceRequestV2Entity =  instanceRequestV2Repository.instanceRequestV2Repo.findById(requestId);

        // act
        instanceRequestV2Repository.delete(instanceRequestV2Entity.get());

        instanceRequestV2Entity =  instanceRequestV2Repository.instanceRequestV2Repo.findById(requestId);

        Optional<InstanceRequestV2ByDayEntity> requestByDay = findInstanceRequestV2ByDayEntity(createTime.truncatedTo(ChronoUnit.DAYS),
                                                                                       requestId);

        //assert
        assertTrue(instanceRequestV2Entity.isEmpty());
        assertTrue(requestByDay.isEmpty());
    }


    @ParameterizedTest
    @ValueSource(ints = {1, 0, -1})
    void restore(int monthOffset) {

        //Arrange
        String requestId = "requestID";
        Instant createTime = TimeUtils.getSameDateTimeOfPreviousMonth(monthOffset);
        String customer = "cust";

        InstanceRequestV2Entity instanceRequestV2 = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(
                requestId, createTime, customer, null);

        // act
        instanceRequestV2Repository.restore(instanceRequestV2);

        Optional<InstanceRequestV2Entity> instanceRequestV2Entity = instanceRequestV2Repository.instanceRequestV2Repo.findById(
                requestId);
        Optional<InstanceRequestV2ByDayEntity> requestByDay = findInstanceRequestV2ByDayEntity(
                createTime.truncatedTo(ChronoUnit.DAYS),
                requestId);

        //assert
        assertTrue(instanceRequestV2Entity.isPresent());
        assertInstanceRequestV2Entity(instanceRequestV2Entity, requestId, customer, createTime);

        assertTrue(requestByDay.isPresent());
        assertEquals(createTime.truncatedTo(ChronoUnit.DAYS),
                     requestByDay.get().getKey().getTruncatedTsByDay());
        assertEquals(requestId, requestByDay.get().getKey().getRequestId());
        assertEquals(instanceRequestV2Entity.get().getCreateTimeuuid(),
                     requestByDay.get().getCreateTimeuuid());
    }


    @Test
    void findByRequestId_findRecord() {

        //Arrange
        String requestIdPrefix = "requestID";
        Instant createTime = Instant.now();
        String customer = "cust";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);

        String requestId = requestIdPrefix + "_2";

        // act
        Optional<InstanceRequestV2Entity> instanceRequestV2Entity =  instanceRequestV2Repository.findRequestById(requestId);

        //assert
        assertTrue(instanceRequestV2Entity.isPresent());
        assertInstanceRequestV2Entity(instanceRequestV2Entity, requestId, customer, null);
    }

    @Test
    void findByRequestId_noRecord() {

        //Arrange
        String requestIdPrefix = "requestID";
        Instant createTime = Instant.now();
        String customer = "cust";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);

        String requestId = requestIdPrefix + "_4"; // does not exist

        // act
        Optional<InstanceRequestV2Entity> instanceRequestV2Entity = instanceRequestV2Repository.findRequestById(requestId);

        //assert
        assertTrue(instanceRequestV2Entity.isEmpty());
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findByRequestIdAndCustomer_findRecord(boolean provideCustomer) {

        //Arrange
        String requestIdPrefix = "requestID";
        Instant createTime = Instant.now();
        String customer = "cust";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);

        String requestId = requestIdPrefix + "_2";

        // act
        Optional<InstanceRequestV2Entity> instanceRequestV2Entity =  instanceRequestV2Repository.findRequestByIdAndCustomer(requestId, provideCustomer ? customer : null);

        //assert
        assertTrue(instanceRequestV2Entity.isPresent());
        assertInstanceRequestV2Entity(instanceRequestV2Entity, requestId, customer, null);
    }


    @Test
    void findByRequestIdAndCustomer_noRecord_wrongCustomer() {

        //Arrange
        String requestIdPrefix = "requestID";
        Instant createTime = Instant.now();
        String customer = "cust";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);

        String requestId = requestIdPrefix + "_2";

        // act
        Optional<InstanceRequestV2Entity> instanceRequestV2Entity =  instanceRequestV2Repository.findRequestByIdAndCustomer(requestId, "wrong_customer_id");

        //assert
        assertTrue(instanceRequestV2Entity.isEmpty());
    }

    @Test
    void findByRequestIdAndCustomer_noRecord_wrongId() {

        //Arrange
        String requestIdPrefix = "requestID";
        Instant createTime = Instant.now();
        String customer = "cust";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);

        String requestId = requestIdPrefix + "_4"; // does not exist

        // act
        Optional<InstanceRequestV2Entity> instanceRequestV2Entity =  instanceRequestV2Repository.findRequestByIdAndCustomer(requestId, customer);

        //assert
        assertTrue(instanceRequestV2Entity.isEmpty());
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findBydRequestIdsAndCustomer_findRecords(boolean provideCustomer) {

        //Arrange
        String requestIdPrefix = "requestID";
        String requestIdPrefix2 = "requestID2";
        Instant createTime = Instant.now();
        String customer = "cust";
        String customer2 = "cust2";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);
        insertRequestsInDb(requestIdPrefix2, 1, createTime, customer2, null);

        ArrayList<String> lookupList = new ArrayList<>();
        lookupList.add(requestIdPrefix + "_1");
        lookupList.add(requestIdPrefix + "_4"); // does not exist
        lookupList.add(requestIdPrefix + "_2");
        lookupList.add(requestIdPrefix2); // another customer

        Set<String> lookupSet = new HashSet<>(lookupList);

        // act
        List<InstanceRequestV2Entity> instanceRequestV2Entities =  instanceRequestV2Repository.findRequestsByIdsAndCustomer(lookupSet, provideCustomer ? customer : null);

        //assert
        assertEquals(provideCustomer ? 2 : 3, instanceRequestV2Entities.size());
        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, lookupList.get(0)), lookupList.get(0), customer, null);
        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, lookupList.get(2)), lookupList.get(2), customer, null);
        if (!provideCustomer) {
            assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, lookupList.get(3)), lookupList.get(3), customer2, null);
        }
    }

    @Test
    void findByRequestIds_findRecords() {

        //Arrange
        String requestIdPrefix = "requestID";
        String requestIdPrefix2 = "requestID2";
        Instant createTime = Instant.now();
        String customer = "cust";
        String customer2 = "cust2";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);
        insertRequestsInDb(requestIdPrefix2, 1, createTime, customer2, null);

        ArrayList<String> lookupList = new ArrayList<>();
        lookupList.add(requestIdPrefix + "_1");
        lookupList.add(requestIdPrefix + "_4"); // does not exist
        lookupList.add(requestIdPrefix + "_2");
        lookupList.add(requestIdPrefix2); // another customer

        Set<String> lookupSet = new HashSet<>(lookupList);

        // act
        List<InstanceRequestV2Entity> instanceRequestV2Entities =  instanceRequestV2Repository.findRequestsByIds(lookupSet);

        //assert
        assertEquals(3, instanceRequestV2Entities.size());

        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, lookupList.get(0)), lookupList.get(0), customer, null);
        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, lookupList.get(2)), lookupList.get(2), customer, null);
        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, lookupList.get(3)), lookupList.get(3), customer2, null);
    }


    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findByRequestIds_noRecords_emptyList(boolean  emptyList) {

        //Arrange
        String requestIdPrefix = "requestID";
        String requestIdPrefix2 = "requestID2";
        Instant createTime = Instant.now();
        String customer = "cust";
        String customer2 = "cust2";

        insertRequestsInDb(requestIdPrefix, 3, createTime, customer, null);
        insertRequestsInDb(requestIdPrefix2, 1, createTime, customer2, null);

        // act
        List<InstanceRequestV2Entity> instanceRequestV2Entities =  instanceRequestV2Repository.findRequestsByIds(emptyList ? new HashSet<>() : null );

        //assert
        assertEquals(0, instanceRequestV2Entities.size());
    }


    @Test
    void findRequestsInRange_findRecords() {

        //Arrange
        List<String> requestIds = RandomFactory.getRandomStringList(5, "requestId");

        Instant createTimeNow = Instant.now();
        Instant createTimeBefore1 = createTimeNow.minus(1, ChronoUnit.DAYS);
        Instant createTimeBefore2 = createTimeNow.minus(2, ChronoUnit.DAYS);
        Instant createTimeAfter1 = createTimeNow.plus(1, ChronoUnit.DAYS);
        Instant createTimeAfter2 = createTimeNow.plus(2, ChronoUnit.DAYS);

        String customer = "cust";
        String customer2 = "cust2";

        insertRequestsInDb(requestIds.get(0), 1, createTimeNow, customer, null);
        insertRequestsInDb(requestIds.get(1), 1, createTimeBefore1, customer, null);
        insertRequestsInDb(requestIds.get(2), 1, createTimeBefore2, customer2, null);
        insertRequestsInDb(requestIds.get(3), 1, createTimeAfter1, customer2, null);
        insertRequestsInDb(requestIds.get(4), 1, createTimeAfter2, customer, null);

        // act
        // createTimeBefore2 and createTimeAfter2 are not included
        List<InstanceRequestV2Entity> instanceRequestV2Entities =  instanceRequestV2Repository.findRequestsPerPeriod(createTimeBefore1, createTimeAfter1);

        //assert
        assertEquals(3, instanceRequestV2Entities.size());

        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, requestIds.get(0)), requestIds.get(0), customer, null);
        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, requestIds.get(1)), requestIds.get(1), customer, null);
        assertInstanceRequestV2Entity(findInstanceRequestV2EntityInList(instanceRequestV2Entities, requestIds.get(3)), requestIds.get(3), customer2, null);
    }

    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void findRequestIdsPerNcaId_findRecords(boolean useDifferentNcaId) {

        //Arrange
        List<String> requestIds = RandomFactory.getRandomStringList(4, "requestId");

        Instant createTimeNow = Instant.now();
        Instant createTimeBefore1 = createTimeNow.minus(1, ChronoUnit.DAYS);
        Instant createTimeBefore2 = createTimeNow.minus(65, ChronoUnit.DAYS); // outside of the period
        Instant createTimeAfter1 = createTimeNow.plus(2, ChronoUnit.DAYS); // outside of the period

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaId", 5);
        String ncaId2 = RandomFactory.getRandomStringWithPrefix("ncaId", 5);

        // has to be returned
        InstanceRequestV2Entity correct1 = insertRequestsInDb(requestIds.get(0), 1, createTimeNow, ncaId, (r) -> {
            r.setNcaId(ncaId);
        }).getFirst();

        // has to be returned only for right ncaId
        InstanceRequestV2Entity correct2 = insertRequestsInDb(requestIds.get(1), 1, createTimeBefore1, ncaId, (r) -> {
            r.setNcaId(useDifferentNcaId ? ncaId2: ncaId);
        }).getFirst();


        Mockito.when(icmsConfigurationProperties.getCancelRequestUpToPastMonths()).thenReturn(1);

        // act
        // createTimeBefore2 and createTimeAfter2 are not included
        List<InstanceRequestV2Entity> requests =  instanceRequestV2Repository.findRequestsPerNcaId(ncaId);


        //assert
        assertEquals(useDifferentNcaId ? 1 : 2, requests.size());
        assertTrue(requests.stream().anyMatch(r -> r.getRequestId().equals(correct1.getRequestId())));
        if (!useDifferentNcaId) {
            assertTrue(requests.stream().anyMatch(r -> r.getRequestId().equals(correct2.getRequestId())));
        }
    }

    @Test
    void findRequestIdsPerNcaIdAndDeploymentId_findRecords() {

        //Arrange
        List<String> requestIds = RandomFactory.getRandomStringList(4, "requestId");
        UUID deploymentId = UUID.randomUUID();

        Instant createTimeNow = Instant.now();
        Instant createTimeBefore1 = createTimeNow.minus(1, ChronoUnit.DAYS);

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaId", 5);

        // has to be returned
        InstanceRequestV2Entity correct1 = insertRequestsInDb(requestIds.get(0), 1, createTimeNow, ncaId, (r) -> {
            r.setNcaId(ncaId);
            r.setDeploymentId(deploymentId);
        }).getFirst();

        // never returned - wrong deployment id
        insertRequestsInDb(requestIds.get(1), 1, createTimeBefore1, ncaId, (r) -> {
            r.setNcaId(ncaId);
        }).getFirst();

        Mockito.when(icmsConfigurationProperties.getCancelRequestUpToPastMonths()).thenReturn(1);

        // act
        // createTimeBefore2 and createTimeAfter2 are not included
        List<InstanceRequestV2Entity> requests =  instanceRequestV2Repository.findRequestsPerNcaIdAndDeploymentId(ncaId, deploymentId);

        //assert
        assertEquals(1, requests.size());
        assertTrue(requests.stream().anyMatch(r -> r.getRequestId().equals(correct1.getRequestId())));
    }

    @Test
    void findAllRequestsAndApplyAction_findRecords() {

        //Arrange
        int numberOfRecords = 4;

        insertRequestsInDb(null, 4, null, null, null);

        // act
        // createTimeBefore2 and createTimeAfter2 are not included
        AtomicInteger count = new AtomicInteger();
        instanceRequestV2Repository.findAllRequestsAndApplyAction(
                r -> {
                        count.getAndIncrement();
                    }, 1, 100, 100);

        //assert
        assertEquals(numberOfRecords, count.get()); // action is called 4 times
    }


    @Test
    void findRequestsInRange_perf() {

        SyncCqlSession fd;
        //Arrange
        Stopwatch stopwatch = Stopwatch.createUnstarted();

        stopwatch.start();

        //insertRequestsInDbInParallel("test", 50000, null, null, null);

        Thread t1 = Thread.ofVirtual().start(() -> {insertRequestsInDbInParallel("test", 10000, null, null, null);});
        //Thread t2 = Thread.ofVirtual().start(() -> {insertRequestsInDbInParallel("test", 50000, null, null, null);});

        try {
           //t2.join();
            t1.join();
        } catch (InterruptedException e) {
            throw new RuntimeException(e);
        }

        long insterSeconds =  stopwatch.elapsed(TimeUnit.SECONDS);

        // act
        // createTimeBefore2 and createTimeAfter2 are not included
        List<InstanceRequestV2Entity> instanceRequestV2Entities =  instanceRequestV2Repository.findRequestsPerPeriod(
                TimeUtils.getFirstDateOfPreviousMonth(3),
                TimeUtils.getFirstDateOfPreviousMonth(-1));
        long findSeconds =  stopwatch.elapsed(TimeUnit.SECONDS);

        //assert
        assertEquals(10000, instanceRequestV2Entities.size());
    }


    List<InstanceRequestV2Entity> insertRequestsInDbInParallel(@Nullable String requestIdPrefix, int numberOfRecords, @Nullable Instant createdTime, @Nullable String customer, UpdateEntity<InstanceRequestV2Entity> action) {
        List<String> requestIds = new ArrayList<>();
        for(int i = 0; i < numberOfRecords; i++) {
            requestIds.add(UUID.randomUUID().toString());
        }

        DbQueryExecutor<String, InstanceRequestV2Entity> dbQueryExecutor = new DbQueryExecutor<>(dbQueryExecutorService);
        return dbQueryExecutor.executeQueries(requestIds,
                                              1000,
                                              1000,
                                              generateInsertRequestsInDb(),
                                              "insertRequestsInDbInParallel");

    }

    private DbQuery<String, InstanceRequestV2Entity> generateInsertRequestsInDb() {
        return (result, parameters, queryIndex, indexStart, indexEnd) -> {
            List<InstanceRequestV2Entity> r = new ArrayList<>();
            Random random = new Random();
            System.out.printf(
                    "generateInsertRequestsInDb queryIndex %d indexStart %d indexEnd %d parameters.size %d%n",
                    queryIndex,
                    indexStart,
                    indexEnd,
                    parameters.size());

            for (int k = indexStart; k < indexEnd; k++) {
                    int daysToPast = random.nextInt(90);
                    Instant createDay = TimeUtils.getCurrentDate().minus(daysToPast, ChronoUnit.DAYS);
                    InstanceRequestV2Entity entity = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(parameters.get(k),
                                                                            createDay,
                                                                            null,
                                                                            null);
                    try {
                        instanceRequestV2Repository.insert(entity);
                        try {
                            Thread.sleep(5);
                        }
                        catch(Exception ignored) {
                        }
                    }
                    catch(Exception e) {
                        System.out.printf("Query %d fails with error %s%n", queryIndex, e.getMessage());
                        instanceRequestV2Repository.insert(entity);
                        throw e;
                    }
                    //r.add(entity);
                Thread.yield();
            }
            result.put(queryIndex, r);
        };
    }

    /**
     * Create and insert multiple numbers to DB
     * @param requestIdPrefix - prefix for request id. Record will have requestIdPrefix_1, requestIdPrefix_2 and  etc. If number of request is 1 then used a request id
     * @param numberOfRecords number of records to insert
     * @param createdTime timestamp for records. Each new record will have 10 ms extra
     * @param customer provide a customer owns this request
     * @param action - extra action to be called when record is created
     */
    List<InstanceRequestV2Entity> insertRequestsInDb(@Nullable String requestIdPrefix, int numberOfRecords, @Nullable Instant createdTime, @Nullable String customer, UpdateEntity<InstanceRequestV2Entity> action) {
        List<InstanceRequestV2Entity> added = new ArrayList<>();
        requestIdPrefix = requestIdPrefix != null ? requestIdPrefix : UUID.randomUUID().toString();
        for(int i = 0; i < numberOfRecords; i++) {
            InstanceRequestV2Entity entity = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(numberOfRecords == 1 ? requestIdPrefix : requestIdPrefix + "_" + i,
                                                                  createdTime != null ? createdTime.plusMillis(i * 10L) : null,
                                                                  customer,
                                                                  action);
            instanceRequestV2Repository.insert(entity);
            added.add(entity);
        }

        return added;
    }

    private Optional<InstanceRequestV2Entity> findInstanceRequestV2EntityInList(List<InstanceRequestV2Entity> instanceRequestV2Entities, String requestId) {
        AtomicReference<InstanceRequestV2Entity> result = new AtomicReference<>();

        instanceRequestV2Entities.forEach((r) ->  {
            if (r.getRequestId().equals(requestId)) { result.set(r); }
        });

        return Optional.of(result.get());
    }


    @Test
    void DatabaseCleanupTask_ok()
            throws InterruptedException {

        // Arrange
        DatabaseCleanupTask<InstanceRequestV2ByDayEntity> task = new DatabaseCleanupTask<>(icmsConfigurationProperties, telemetryEventClient, lockProviderService);
        RequestsByDayCleanupExecutor executor = new RequestsByDayCleanupExecutor(instanceRequestV2Repository, terminateInstanceService, icmsConfigurationProperties);

        Mockito.when(icmsConfigurationProperties.isDatabaseCleanupTaskEnabled()).thenReturn(true);
        Mockito.when(icmsConfigurationProperties.getDatabaseCleanupLookupPeriodInDays()).thenReturn(10);
        Mockito.when(icmsConfigurationProperties.getDatabaseRecordsTtlInDays()).thenReturn(3);
        Mockito.when(icmsConfigurationProperties.getDatabaseCleanupLockTtlInSeconds()).thenReturn(15);
        Mockito.when(icmsConfigurationProperties.getDatabaseCleanupDbPageSize()).thenReturn(1); // to test that paging works

        // These records will be deleted before job stats so the entire records for this day will be removed
        Instant dayDeleted =  Instant.now().truncatedTo(ChronoUnit.DAYS);
        List<InstanceRequestV2Entity> newRecordsToDelete = insertRequestsInDb("db_cleanup_deleted", 2, dayDeleted, null,null);
        newRecordsToDelete.forEach(r -> instanceRequestV2Repository.delete(r));

        //These records are day that is beyond keep period which is 3 days. All records will be removed by job
        Instant dayExpired =  TimeUtils.getPreviousDate(5);
        List<InstanceRequestV2Entity> newExpiredRecords = insertRequestsInDb("db_cleanup_expired", 2, dayExpired, null, null);

        List<InstanceRequestV2Entity> recordsToKeep = insertRequestsInDb("db_cleanup", 2, TimeUtils.getPreviousDate(1), null,null);

        //Act
        //Validate that new record can be created
        InstanceRequestV2ByDayEntity v = new InstanceRequestV2ByDayEntity( new InstanceRequestV2ByDayKey(Instant.now(), "sdfsds"), UUID.randomUUID(), null);

        task.execute(executor, "testJob");

        // Assert
        Set<String> requestIds = new HashSet<>();

        newRecordsToDelete.forEach(r -> requestIds.add(r.getRequestId()));
        newExpiredRecords.forEach(r -> requestIds.add(r.getRequestId()));
        List<InstanceRequestV2Entity> requests = instanceRequestV2Repository.findRequestsByIds(requestIds);

        assertTrue(requests.isEmpty()); // records should be deleted from Db

        List<InstanceRequestV2ByDayEntity> requestsByDay = instanceRequestV2Repository.instanceRequestV2ByDayRepo.findAllByKeyTruncatedTsByDay(dayDeleted, Limit.unlimited());
        assertTrue(requestsByDay.isEmpty()); // this table should not have any records for this day

        requestsByDay = instanceRequestV2Repository.instanceRequestV2ByDayRepo.findAllByKeyTruncatedTsByDay(dayExpired, Limit.unlimited());
        assertTrue(requestsByDay.isEmpty()); // this table should not have any records for this day

        requestIds.clear();
        recordsToKeep.forEach(r -> requestIds.add(r.getRequestId()));
        requests = instanceRequestV2Repository.findRequestsByIds(requestIds);

        assertEquals(2, requests.size());

        List<InstanceRequestV2Entity> finalRequests = requests;
        recordsToKeep.forEach(r -> {
            assertTrue(finalRequests.stream().anyMatch(r2 -> Objects.equals(r2.getRequestId(),
                                                                            r.getRequestId())));
        });
    }


    private Optional<InstanceRequestV2ByDayEntity> findInstanceRequestV2ByDayEntity(Instant createTime, String requestId)
    {
        Optional<InstanceRequestV2ByDayEntity> requestByDay =  instanceRequestV2Repository.instanceRequestV2ByDayRepo.findByKeyTruncatedTsByDayAndKeyRequestId(
                createTime.truncatedTo(ChronoUnit.DAYS),
                requestId);
        if (requestByDay.isPresent() && requestByDay.get().isMarkedAsDeleted()) {
            requestByDay = Optional.empty();
        }
        return requestByDay;
    }
}
