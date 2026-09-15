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
package com.nvidia.ess.util;

import static org.junit.jupiter.api.Assertions.assertNotNull;

import jakarta.annotation.Nullable;
import java.util.Objects;
import lombok.Getter;
import lombok.ToString;
import reactor.core.publisher.Mono;

@Getter
@ToString
public class ResultOrErrorOrEmpty<T> {

  @Nullable
  private T resultOrEmpty;

  @Nullable
  private Exception error;

  private ResultOrErrorOrEmpty(T resultOrEmpty) {
    this.resultOrEmpty = resultOrEmpty;
  }

  private ResultOrErrorOrEmpty(Exception error) {
    assertNotNull(error);
    this.error = error;
  }

  public static <T> ResultOrErrorOrEmpty<T> fromResultOrEmpty(T resultOrEmpty) {
    return new ResultOrErrorOrEmpty<>(resultOrEmpty);
  }

  public static <T> ResultOrErrorOrEmpty<T> fromError(Exception error) {
    return new ResultOrErrorOrEmpty<>(error);
  }

  public boolean isError() {
    return this.error != null;
  }

  public boolean hasResult() {
    return !isError() && !Objects.isNull(resultOrEmpty);
  }

  public Mono<T> asMono() {
    if (isError()) {
      return Mono.error(error);
    }
    return Objects.isNull(resultOrEmpty) ? Mono.empty() : Mono.just(resultOrEmpty);
  }
}
