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
package com.nvidia.nvct.grpc;

import static org.assertj.core.api.AssertionsForClassTypes.assertThat;

import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.proto.ResultMetadataResponse;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;
import java.util.UUID;
import java.util.concurrent.CountDownLatch;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class TestResultMetadataStreamObserver
        implements StreamObserver<ResultMetadataResponse> {

    private final CountDownLatch finishLatch;
    private final Status.Code expectedStatus;
    private final UUID taskId;
    private final TaskStatus taskStatus;

    public TestResultMetadataStreamObserver(UUID taskId,
                                            TaskStatus taskStatus,
                                            CountDownLatch finishLatch,
                                            Status.Code expectedStatus) {
        this.finishLatch = finishLatch;
        this.expectedStatus = expectedStatus;
        this.taskId = taskId;
        this.taskStatus = taskStatus;
    }

    @Override
    public void onNext(ResultMetadataResponse resultMetadataResponse) {
        assertThat(resultMetadataResponse).isNotNull();
        log.debug("Received metadata response '{}'", resultMetadataResponse);
        assertThat(taskId).hasToString(resultMetadataResponse.getTaskId());
        assertThat(taskStatus).hasToString(resultMetadataResponse.getExecutionStatus());
    }

    @Override
    public void onError(Throwable throwable) {
        log.error("Encountered error in TestResultMetadataStreamObserver ", throwable);
        var status = Status.fromThrowable(throwable).getCode();
        assertThat(status).isEqualTo(expectedStatus);
        finishLatch.countDown();
    }

    @Override
    public void onCompleted() {
        log.debug("Finished ResultMetadataRequest");
        finishLatch.countDown();
    }
}
