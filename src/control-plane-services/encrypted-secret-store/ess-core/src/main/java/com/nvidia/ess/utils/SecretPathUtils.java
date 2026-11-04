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
package com.nvidia.ess.utils;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.ess.exceptions.AnomalyException;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.stream.Collectors;
import lombok.Builder;
import lombok.Builder.Default;
import lombok.Getter;
import lombok.NonNull;
import lombok.Setter;
import org.apache.commons.lang3.CharSetUtils;
import org.apache.commons.lang3.StringUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

public class SecretPathUtils {

  /**
   * Remove any leading & trailing whitespace, squeeze any consecutive slashes (slashes are the path-delimiters)
   * and remove any trailing slashes.
   * 
   * @param path
   * @return
   */
  public static String sanitizePath(@NonNull String path) {
    path = CharSetUtils.squeeze(StringUtils.strip(path), "/");
    return "/".equals(path) ?  "/" : StringUtils.stripEnd(path, "/");
  }

  /**
   * 
   * Check whether the two given paths are equivalent, after sanitizing both
   * (see {@link SecretPathUtils#sanitizePath(String)}).
   * 
   * @param path1
   * @param path2
   * @return
   */
  public static boolean pathsAreSame(@NonNull String path1, @NonNull String path2) {
    return Objects.equals(sanitizePath(path1), sanitizePath(path2));
  }

  /**
   * 
   * <p>Split the given path-string into its constituent path-elements. {@code /} is the delimiter
   * used. The path-string is first sanitized using {@link SecretPathUtils#sanitizePath(String)}.</p>
   * 
   * <p>If the path-string is blank (empty-string or whitespace-only string), an empty list of path-element
   * ({@code []}) is returned.</p>
   * 
   * <p>If the path-string leads with a {@code /}, the first path-element is an empty-string (i.e.
   * {@code ["", ...]}). This is to distinguish this path-string from path-strings that don't start with
   * a leading {@code /}. The {@code "/"} path-string itself has only one empty-string path-element
   * (i.e. {@code [""]}).</p>
   * 
   * @param path
   * @return
   */
  private static List<String> pathElements(@NonNull String path) {
    if (StringUtils.isBlank(path)) {
      // Empty / blank "path-string": No path-elements ([]).
      return List.of();
    }

    // Sanitize the path (remove leading & trailing whitespace, squeeze consecutive slashes,
    // no trailing slashes unless path is the "/" path-string).
    path = sanitizePath(path);

    // Pythonic `str.split(char)` semantics that neither `String.split` not `StringUtils.split(String)`
    // seem to replicate (empty-string-split-elements that precede a string-leading delimiter are ignored
    // by the latter two).
    var res = new ArrayList<String>();
    var currElement = new StringBuilder();
    for (int i = 0; i < path.length(); ++i) {
      switch (path.charAt(i)) {
        case '/':
          // Start a new path-element upon encountering a delimiter. Paths leading
          // with '/' have an empty path-element at the beginning to differentiate them from
          // paths not leading with '/'.
          res.add(currElement.toString());
          currElement = new StringBuilder();
          break;
        default:
          currElement.append(path.charAt(i));
      }
    }
    
    if (!currElement.isEmpty()) {
      // Add the trailing path-element. Only the "/" path-string would result in an empty trailing
      // path-element after an empty leading path-element; the trailing path-element is not added
      // in this case (thus the "/" path-string's path-elements are effectively: [""]).
      res.add(currElement.toString());
    }

    return res;
  }

  /**
   * 
   * <p>Determine whether {@code path1} is a path-prefix of {@code path2}.
   * If {@code path1} is equivalent to {@code path2}, then {@code path1} is
   * still treated as a path-prefix of {@code path2}.</p>
   * 
   * <p>Slashes ({@code /}) are treated as the path-delimiters within either path.</p>
   * 
   * @param path1
   * @param path2
   * @return
   */
  public static boolean isPathPrefixOf(@NonNull String path1, @NonNull String path2) {
    // If one of the paths is blank and the other isn't, then clearly one path isn't
    // the prefix of the other. Perform this check early so that empty-string("") 'paths'
    // (whose list of path-elements would be an empty-list) aren't mistaken as prefixes of
    // all other paths further below.
    if (!Objects.equals(StringUtils.isBlank(path1), StringUtils.isBlank(path2))) {
      return false;
    }

    // Split both paths into their respective lists of path-elements. Paths leading with
    // a '/' (including the "/" path-string itself) will have a single empty-string("")
    // path-element at the beginning to distinguish them from paths not leading with a '/'.
    var pathElements1 = pathElements(path1);
    var pathElements2 = pathElements(path2);

    // A path-prefix cannot have more path-elements than the path itself.
    if (pathElements1.size() > pathElements2.size()) {
      return false;
    }
    // Compare path-elements of both paths from left-to-right until the would-be
    // path-prefix has exhausted its path-elements.
    for (int i = 0; i < pathElements1.size(); ++i) {
      if (!pathElements1.get(i).equals(pathElements2.get(i))) {
        return false;
      }
    }
    return true;
  }

  /**
   * 
   * <p>Given a secret-path, return all its path-prefixes (including the secret-path itself) as a map.
   * The keys of the map are the prefixes, and the value against each key is {@code true} if that key
   * is a directory-path, and {@code false} if that key is the secret-path itself.</p>
   * 
   * <p><b>IMPORTANT NOTE 1:</b> Whitespace that is neither leading nor trailing is assumed to be part of the
   * path-string itself, while leading and trailing whitespace is ignored. Consecutive occurrences of the
   * path-delimiter {@code /} are squeezed into a single path-delimiter and any trailing path-delimiter is
   * ignored.</p>
   *
   * <p><b>IMPORTANT NOTE 2:</b> If any individual path-elements within the given secret-path contain the
   * path-delimiter (i.e. {@code /}) themselves, that path-delimiter <b>MUST</b> be encoded in some form
   * (e.g. URL-encoding) and <b>MUST NOT</b> be present as a {@code /} in the given path (even if backslash-escaped).
   * This function does not assume that the string follows any <b>particular</b> encoding or escape conventions.</p>
   * 
   * <p><b>IMPORTANT NOTE 3:</b> This function confers no special meaning on path elements like {@code ..} or {@code .}.
   * These are not interpreted as redirects to the current or parent path-element, but as path-elements in their
   * own right just like any other.</p>
   *
   * @param secretPath
   * @return
   */
  public static Map<String, Boolean> getAllSecretPathPrefixes(@NonNull String secretPath) {

    Map<String, Boolean> allPrefixes = new HashMap<>();

    // Split the given path into its path-elements.
    var pathElements = pathElements(secretPath);

    if (pathElements.isEmpty()) {
      // Path-string is blank.
      return allPrefixes;
    }

    // Form all non-empty path-prefixes and add them to the map.
    var pathPrefixBuilder = new StringBuilder();

    int start = 0;
    if (StringUtils.isBlank(pathElements.get(0))) {
      // First path-element is empty. Path leads with a '/'. All subsequent path-elements
      // are guaranteed to be non-empty. Skip this empty path-element and add the '/' to all
      // path-prefixes.
      start = 1;
      pathPrefixBuilder.append('/');
      if (pathElements.size() == 1) {
        // Only case where `pathElements == [""]` is when the secret-path-string is just "/".
        // Add "/" to the `allPrefixes` map here as the loop below won't be traversed.
        //
        // Note: This path-string is still treated as a secret-path in this function as it is
        // specified as such.
        allPrefixes.put(pathPrefixBuilder.toString(), false);
      }
    }

    for (var i = start; i < pathElements.size(); ++i) {
      if (i > start) {
        // Not the first [non-empty] path-element. Add the delimiting '/' before adding the
        // next path-element to form the next path-prefix.
        pathPrefixBuilder.append('/');
      }
      pathPrefixBuilder.append(pathElements.get(i));
      allPrefixes.put(pathPrefixBuilder.toString(), i + 1 < pathElements.size());
    }

    return allPrefixes;
  }

  /**
   * 
   * <p><b>This function performs path-validations prior to inserting a secret at a specific path and then returns a
   * {@link SecretPathWriteArgs} instance</b> (as a {@link Mono}) containing all the rows to be inserted to the
   * {@code secret_paths_by_entity} table prior to the insertion of the secret itself, along with information necessary
   * to perform a CAS write to change the {@code entity_version} column-value as part of these insertions. In order to
   * power this, the function does the following:</p>
   * 
   * <p>1) Takes in the (reactive) result of a fetch-attempt for all existing path-prefixes for a secret from
   * the {@code secret_paths_by_entity} table (passed as {@code Flux<SecretPathModel> allFetchedPaths}).</p>
   * 
   * <p>2) Validates whether the row for each path-prefix that already exists in the table has its {@code is_dir}
   * column set correctly ({@code false} for the secret-path itself and {@code true}for any ancestor-directory-path).</p>
   * 
   * <p>3) Forms a list of path rows (secret-path & directory-paths) that need to be inserted into the
   * {@code secret_paths_by_entity} table before the secret can be inserted so that all missing paths are in. This
   * is the field {@code List<SecretPathModel> SecretPathWriteArgs.pathsToInsert}) in the constructed
   * {@link SecretPathWriteArgs} instance.</p>
   * 
   * <p>4) If at least one of the expected paths was present in the {@code secret_paths_by_entity} table, obtain
   * the {@code entity_version} column-value that needs to be checked with CAS and updated with a new value
   * (see [4] below) as part of the insertion of the rows identified in [3] above. This is the field
   * {@code UUID SecretPathWriteArgs.prevEntityVersionForCAS} in the constructed
   * {@link SecretPathWriteArgs} instance. If the fetch for all existing path-prefixes (i.e. the
   * {@code Flux<SecretPathModel> allFetchedPaths} function-argument) returned nothing, this field will be
   * set to {@code null}, and a separate, earlier fetch of the current value of the {@code entity_version}
   * column would be needed further downstream to apply the CAS operation which updates the {@code entity_version}
   * column with a new value (see [4] below) as part of the insertion of the rows identified in [3] above.</p>
   * 
   * <p>5) The new value of the {@code entity_version} column that needs to be set (in the CAS operation detailed in
   * [4] above), as part of the insertion of the rows identified in [3] above. This is the field
   * {@code UUID SecretPathWriteArgs.newEntityVersion} in the constructed {@link SecretPathWriteArgs}
   * instance.</p>
   * 
   * @param namespace Namespace within which the secret is to be inserted.
   * 
   * @param entity Entity (@code "&lt;entity_type&gt;/&lt;entity_id&gt;") under the namespace within which the secret is to
   *               be inserted.
   * 
   * @param allFetchedPaths Result of a (reactive) fetch of rows for all path-prefixes of the secret to be
   *                        inserted. This will be validated against the {@code Map<String, Boolean> allPathPrefixes}
   *                        argument.
   * 
   * @param allPathPrefixes The keys of this map are all the path-prefixes of the secret-path (including itself),
   *                        and the value against each key is {@code true} if that key should be a directory-path,
   *                        and {@code false} if that key is the secret-path itself.
   * 
   * @return A {@link SecretPathWriteArgs} instance as a {@link Mono} once all validation is complete.
   */
  public static Mono<SecretPathWriteArgs> validateAndGetInsertionArgs(String namespace, String entity,
      Flux<SecretPathModel> allFetchedPaths, Map<String, Boolean> allPathPrefixes) {

    return allFetchedPaths
        .flatMap(fetchedPath -> {
          var fetchedPathStr = fetchedPath.getPath();
          var isDirActual = fetchedPath.getIsDir();
          var isDirExpected = allPathPrefixes.get(fetchedPathStr);
          if (isDirExpected == null) {
            // NOTE: This shouldn't happen. If it does, it's an error in the caller of this function and should
            // be caught and patched accordingly. AnomalyException instances should result in a 500 error
            // being sent to the client but the exception-messae should only be logged and the error-response
            // itself should be generic.
            return Mono.error(new AnomalyException(String.format(
                "encountered unrecognized path-prefix: '%s' while trying to validate secret-paths under %s",
                fetchedPathStr, LogMessageStringUtils.namespaceEntityTuple(namespace, entity))));
          }
          if (!isDirExpected.equals(isDirActual)) {
            // Either an ancestor-directory of the secret-path is a secret-path in the DB, or the secret-path itself
            // is a directory in the DB. Fail validation.
            //
            // This is not a retryable error.
            return Mono.error(new ConflictException(String.format("found %s at '%s' while trying to validate secret-paths already existing under %s",
                    Boolean.TRUE.equals(isDirActual) ? "directory" : "secret-path", fetchedPathStr,
                    LogMessageStringUtils.namespaceEntityTuple(namespace, entity))));
          }
          return Mono.just(fetchedPath);
        })
        // Even if `allFetchedPaths` yields an empty sequence, downstream should still execute thanks to
        // `collectList()` generating a Mono({empty-list}).
        .collectList()
        .flatMap(fetchedPathList -> {
          // If at least one path-prefix of the secret-path exists in the DB, obtain the `entity_version` column
          // value for use in the CAS-check from it. If no such path-prefix exists in the DB (i.e. all identified
          // path-prefixes need to be inserted in the DB as they don't yet exist there), use an `entity_version`
          // value that was fetched independently (that fetch should have finished before this row-fetch started).
          //
          var prevEntityVersionForCAS = fetchedPathList.stream()
              .map(SecretPathModel::getEntityVersion)
              .findFirst();
          
          // Generate the new value of `entity_version` to be set via CAS as part of the insertion.
          var newEntityVersion = Uuids.timeBased();

          // Now obtain the list of all `SecretPathModel` rows that need to be inserted before the insertion of the
          // secret itself, because they don't yet exist in the DB.
          var fetchedPathStrs = fetchedPathList.stream()
              .map(fetchedPath -> fetchedPath.getPath())
              .collect(Collectors.toSet());
          var updatedAt = Uuids.timeBased();
          var pathsToInsert = allPathPrefixes.entrySet()
              .stream()
              .filter(pathPrefixAndIsDirExpected -> !fetchedPathStrs.contains(pathPrefixAndIsDirExpected.getKey()))
              .map(pathPrefixAndIsDirExpected -> SecretPathModel.builder()
                      .namespace(namespace)
                      .entity(entity)
                      .path(pathPrefixAndIsDirExpected.getKey())
                      .entityVersion(newEntityVersion)
                      .updatedAt(updatedAt)
                      .isDir(pathPrefixAndIsDirExpected.getValue())
                      .build())
              .toList();
          // Construct the `SecretPathWriteArgs` return-value.
          return Mono.just(
            SecretPathWriteArgs.builder()
                // The rows to be inserted into the `secret_paths_by_entity` table.
                .pathsToWrite(pathsToInsert)
                // The new value of `entity_version` to be set via CAS as part of the insertion above.
                .newEntityVersion(newEntityVersion)
                // If the fetch of existing paths was nonempty, the value of `entity_version` to
                // check for while performing CAS as part of the insertion above. Otherwise, a value of
                // `entity_version` fetched before the existing-paths fetch started should be used instead.
                .prevEntityVersionForCAS(prevEntityVersionForCAS.orElse(null))
                .build());
        });
  }

  @Builder
  @Getter
  private static final class AdjPathsandPathPrefixes {
    @NonNull
    @Default
    @Setter
    private String leftAdjPath = "";

    @NonNull
    @Default
    @Setter
    private String rightAdjPath = "";

    @NonNull
    @Default
    private final Map<String, SecretPathModel> pathPrefixes = new HashMap<>();

    @Setter
    private SecretPathModel secretPath;
  }

  /**
   * 
   * <p>Check whether the given {@code secretPathToDelete} exists in the {@code secret_paths_by_entity} table
   * (the list of all paths in the entity is given by the {@link Flux} instance {@code allPathsInEntity}),
   * and if it does, obtain it and any ancestor directory-paths existing in the table that contain no other
   * secret-paths besides it for deletion.</p>
   * 
   * <p>If the {@code secretPathToDelete} doesn't exist in the table, a {@link Mono#empty()} is returned.</p>
   * 
   * <p>Any exceptions surfaced during the all-paths-in-entity fetch (i.e. exceptions from the {@link Flux} instance
   * {@code allPathsInEntity}) are echoed downstream.</p>
   * 
   * <p>If a non-empty list of paths to delete has been derived, the value of the partition's {@code entity_version}
   *  as of the existence of these paths is also returned in order to form an LWT condition to guard a downstream
   * delete-transaction for these paths. A freshly generated new {@code timeuuid} value of {@code entity_version}
   * is also returned to set as part of this downstream transaction.</p>
   * 
   * @param allPathsInEntity
   * @param secretPathToDelete
   * @return
   */
  public static Mono<SecretPathWriteArgs> getSecretPathAndEmptyDirectoriesToDelete(@NonNull Flux<SecretPathModel> allPathsInEntity,
      @NonNull String secretPathToDelete) {

    return allPathsInEntity
        .reduce(AdjPathsandPathPrefixes.builder().build(), (adjPathsandPathPrefixes, currPath) -> {

          if (Objects.isNull(currPath.getPath())) {
            // Ignore this "row". It represents an entity-path partition that previously had paths that
            // were subsequently deleted, leaving behind non-null static-columns and partition-keys.
            return adjPathsandPathPrefixes;
          }

          if (!Boolean.TRUE.equals(currPath.getIsDir()) && pathsAreSame(currPath.getPath(), secretPathToDelete)) {
            // Found in DB: Secret-path.
            // Retain the `SecretPathModel` as the associated entity needs to be deleted from the DB.
            adjPathsandPathPrefixes.setSecretPath(currPath);

          } else if (Boolean.TRUE.equals(currPath.getIsDir()) && isPathPrefixOf(currPath.getPath(), secretPathToDelete)) {
            // Found in DB: Directory-path that's a prefix of the given secret-path.
            // Retain the `SecretPathModel` as the associated entity needs to be deleted from the DB.
            adjPathsandPathPrefixes.getPathPrefixes().put(currPath.getPath(), currPath);

          } else if (!Boolean.TRUE.equals(currPath.getIsDir())) {
            // For all other non-directory (i.e. secret) paths found in the DB for this entity:

            if (currPath.getPath().compareTo(secretPathToDelete) < 0 &&
                (StringUtils.isBlank(adjPathsandPathPrefixes.getLeftAdjPath()) ||
                  adjPathsandPathPrefixes.getLeftAdjPath().compareTo(currPath.getPath()) < 0)) {
              // Find LAST_IN_LEXICOGRAPHICAL_ORDER(
              //          ALL_FOUND_SECRET_PATHS_MATCHING(
              //              found-secret-path AHEAD_OF secret-path-to-be-deleted
              //          )
              //      )
              //
              // This is the secret-path that is left-adjacent to the secret-path-to-be-deleted, in
              // lexicographical order.
              adjPathsandPathPrefixes.setLeftAdjPath(currPath.getPath());

            } else if (currPath.getPath().compareTo(secretPathToDelete) > 0 &&
                        (StringUtils.isBlank(adjPathsandPathPrefixes.getRightAdjPath()) ||
                          adjPathsandPathPrefixes.getRightAdjPath().compareTo(currPath.getPath()) > 0)) {
              // Find FIRST_IN_LEXICOGRAPHICAL_ORDER(
              //          ALL_FOUND_SECRET_PATHS_MATCHING(
              //              found-secret-path BEHIND secret-path-to-be-deleted
              //          )
              //      )
              //
              // This is the secret-path that is right-adjacent to the secret-path-to-be-deleted, in
              // lexicographical order.
              adjPathsandPathPrefixes.setRightAdjPath(currPath.getPath());
            }
          }

          return adjPathsandPathPrefixes;
        })
        .flatMap(adjPathsandPathPrefixes -> {
          if (Objects.isNull(adjPathsandPathPrefixes.getSecretPath())) {
            // Secret-path doesn't exist in DB. Nothing to delete.
            return Mono.empty();
          }

          // Obtain the path-prefixes of `leftAdjPath` and `rightAdjPath`.
          //
          // NOTE: If `leftAdjPath` or `rightAdjPath` are blank, `getAllSecretPathPrefixes()` should return
          // an empty collection in either case.
          var leftAdjPathPrefixes = getAllSecretPathPrefixes(adjPathsandPathPrefixes.getLeftAdjPath());
          var rightAdjPathPrefixes = getAllSecretPathPrefixes(adjPathsandPathPrefixes.getRightAdjPath());

          // No path-prefixes of `leftAdjPath` and `rightAdjPath` should be deleted (even if any of them are also
          // path-prefixes of the secret-path-to-be-deleted) as both `leftAdjPath` and `rightAdjPath` will remain
          // even after deleting the secret-path-to-be-deleted.
          //
          // The remaining path-prefixes of the secret-path-to-be-deleted would become empty directories
          //  after the deletion of the secret-path-to-be-deleted, and are therefore safe to clean up.
          //
          // NOTE: It is unnecessary to check for path-prefixes common between the secret-path-to-be-deleted and
          // ANY OTHER secret-path besides `leftAdjPath` and/or `rightAdjPath`, as any path-prefix shared
          // between the secret-path-to-be-deleted and ANY OTHER secret-path in the entity will also be shared
          // by at least one of `leftAdjPath` or `rightAdjPath`.
          //
          var pathsToDelete = adjPathsandPathPrefixes.getPathPrefixes()
              .entrySet()
              .stream()
              .filter(pathAndModel -> !leftAdjPathPrefixes.containsKey(pathAndModel.getKey()) && !rightAdjPathPrefixes.containsKey(pathAndModel.getKey()))
              .map(Map.Entry::getValue)
              .collect(Collectors.toCollection(() -> new ArrayList<>()));

          // Include the secret-path-to-be-deleted as one of the paths to be deleted.
          pathsToDelete.add(adjPathsandPathPrefixes.getSecretPath());

          var deletionArgs = SecretPathWriteArgs.builder()
              // Generate a new value of `entity_version` to accompany the deletion.
              .newEntityVersion(Uuids.timeBased())
              // Obtain the previous value of `entity_version` from the secret-path row, with which
              // to construct an `IF` condition for the LWT that will change `entity_version` to its
              // new value, accompanying the deletion itself.
              .prevEntityVersionForCAS(adjPathsandPathPrefixes.getSecretPath().getEntityVersion())
              .pathsToWrite(pathsToDelete)
              .build();

          return Mono.just(deletionArgs);
        });
  }
}
