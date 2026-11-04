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
package com.nvidia.nvcf.rest.function.invocation;

import static com.nvidia.nvcf.service.worker.WorkerNatsService.getRegionalStreamName;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static org.assertj.core.api.Assertions.assertThat;

import com.google.protobuf.InvalidProtocolBufferException;
import com.nvidia.nvcf.proto.WorkerConnect;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.proto.WorkerGrpc.WorkerBlockingStub;
import com.nvidia.nvcf.proto.WorkerGrpc.WorkerStub;
import com.nvidia.nvcf.proto.WorkerInvokeFunctionRequest;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import io.nats.client.Connection;
import io.nats.client.ConsumerContext;
import io.nats.client.FetchConsumeOptions;
import io.nats.client.Message;
import jakarta.annotation.Nullable;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.function.BiConsumer;
import java.util.function.Supplier;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Slf4j
public class TestWorker implements AutoCloseable {

    private final UUID functionId;
    private final UUID versionId;
    private final int grpcServerPort;
    private final Supplier<String> tokenSupplier;
    private final Connection natsConnection;
    private final BiConsumer<TestWorker, WorkerInvokeFunctionRequest> doWorkFunc;
    private final CountDownLatch ready = new CountDownLatch(1);

    @Getter
    private final CompletableFuture<Void> runningWorkerTask;

    @Getter
    @Nullable
    private WorkerBlockingStub workerStub;
    @Getter
    @Nullable
    private WorkerStub observerWorkerStub;
    @Nullable
    private ManagedChannel channel;

    public TestWorker(
            UUID functionId, UUID versionId, int grpcServerPort, Supplier<String> tokenSupplier,
            Connection natsConnection,
            BiConsumer<TestWorker, WorkerInvokeFunctionRequest> doWorkFunc) {
        this.functionId = functionId;
        this.versionId = versionId;
        this.grpcServerPort = grpcServerPort;
        this.tokenSupplier = tokenSupplier;
        this.natsConnection = natsConnection;
        this.doWorkFunc = doWorkFunc;
        this.runningWorkerTask = CompletableFuture.runAsync(this::run,
                                                            Executors.newSingleThreadExecutor());
    }

    @SneakyThrows
    private void run() {
        log.info("worker running");
        String resultSetToken = tokenSupplier.get();
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + resultSetToken);
        this.channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();

        this.workerStub = WorkerGrpc.newBlockingStub(channel);
        this.observerWorkerStub = WorkerGrpc.newStub(channel);
        var connect = workerStub.connectOnce(WorkerConnect.newBuilder()
                                                     .setFunctionId(
                                                             functionId.toString())
                                                     .setFunctionVersionId(
                                                             versionId.toString())
                                                     .setInstanceId("local-instance")
                                                     .build());
        assertThat(connect).isNotNull();
        assertThat(connect.getNvcfWorkerToken()).isNotBlank();
        log.info("worker connected");
        ready.countDown();
        var js = natsConnection.jetStream();
        var streamName = getRegionalStreamName(connect.getConnectedRegion(), versionId);
        var consumerName = streamName + "_workers";
        var consumer = js.getConsumerContext(streamName, consumerName);
        Flux.defer(() -> Flux.fromIterable(fetchOneWaiting(consumer)))
                .repeat(0)
                .map(message -> {
                    try {
                        return WorkerInvokeFunctionRequest.parseFrom(message.getData());
                    } catch (InvalidProtocolBufferException e) {
                        throw new RuntimeException(e);
                    }
                })
                .doOnNext(next -> log.info("received work request: {}", next))
                .flatMap(message -> {
                    doWorkFunc.accept(this, message);
                    return Mono.empty();
                }).then().block(Duration.ofSeconds(30));
        log.info("worker shutting down");
        channel.shutdownNow();
        log.info("worker shut down");
    }

    @SneakyThrows
    public void waitForReady() {
        ready.await();
    }

    @Override
    @SneakyThrows
    public void close() {
        if (channel != null) {
            channel.shutdownNow();
            channel.awaitTermination(1, TimeUnit.SECONDS);
        }
    }

    @SneakyThrows
    private static List<Message> fetchOneWaiting(ConsumerContext consumerContext) {
        var fco = FetchConsumeOptions.builder()
                .maxMessages(1)
                .expiresIn(10_000)
                .build();
        return fetchMessages(consumerContext, fco);
    }

    @SneakyThrows
    private static List<Message> fetchMessages(
            ConsumerContext consumerContext, FetchConsumeOptions fco) {
        var messages = new ArrayList<Message>(fco.getMaxMessages());
        try (var fetchConsumer = consumerContext.fetch(fco)) {
            while (!fetchConsumer.isFinished()) {
                var message = fetchConsumer.nextMessage();
                if (message != null) {
                    messages.add(message);
                }
            }
            return messages;
        }
    }
}
