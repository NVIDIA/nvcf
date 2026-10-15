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

import static com.nvidia.ess.util.PathWriteArgsTestInstance.newPathFactory;
import static org.junit.jupiter.api.Assertions.assertFalse;

import com.nvidia.ess.utils.SecretPathUtils;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Objects;
import java.util.Random;
import java.util.UUID;
import java.util.stream.Stream;

public class PathDeletionTestUtils {

  public static Stream<PathWriteArgsTestInstance.Path> formSecretAndAncestorPathsForSecrets(List<List<String>> pathStrLists,
      UUID entityVersion, boolean selectSecretPathsAtRandom) {

    var newPath = newPathFactory(entityVersion);

    var allPathStrs = new HashMap<String, Boolean>();

    var rand = new Random();
    for (var pathList : pathStrLists) {
      var numPaths = selectSecretPathsAtRandom ? rand.nextInt(pathList.size()) + 1 : pathList.size();
      List<String> finalPathList = new ArrayList<>();
      finalPathList.addAll(pathList);
      if (selectSecretPathsAtRandom) {
        Collections.shuffle(finalPathList, rand);
      }
      
      finalPathList.stream().limit(numPaths).forEach(pathStr -> {
        var pathPrefixes = SecretPathUtils.getAllSecretPathPrefixes(pathStr);
        for (var pathPrefixAndIsDir : pathPrefixes.entrySet()) {
          var alreadyScannedPathPrefix = allPathStrs.get(pathPrefixAndIsDir.getKey());
          assertFalse(
              !Objects.isNull(alreadyScannedPathPrefix) &&
                  !Objects.equals(pathPrefixAndIsDir.getValue(), alreadyScannedPathPrefix),
              String.format("Malformed test-input. Test-path: '%s' collides with some other test-path", pathStr)
          );
          allPathStrs.put(pathPrefixAndIsDir.getKey(), pathPrefixAndIsDir.getValue());
        }
      });
    }

    return allPathStrs.entrySet()
        .stream()
        .map(pathAndIsDir -> newPath.apply(pathAndIsDir.getKey(), pathAndIsDir.getValue()));
  }
}
