# BAZEL.md Requirements For Maven Users

Use this reference for every application profile. Write `BAZEL.md` for a
developer who understands Maven but is new to Bazel. Use the target project's
real module names, targets, tests, profile, ports, mounts, and CI variables.

## Explain The Configuration Files

Use simple language near the top of `BAZEL.md`:

- `.bazelversion` selects the exact Bazel release. Bazelisk reads it so local
  and CI builds use the same version.
- `.bazelrc` stores repository-wide defaults applied to Bazel commands, such as
  Java 25, workspace-status metadata, and
  `build --java_header_compilation=false`. It is configuration, not a lockfile.
  It must not contain credentials or workstation-specific absolute paths.
  Explain that a line beginning with `common` applies to query, build, test,
  and other commands, while a line beginning with `build` applies only to
  build and commands that inherit build options. Put the `rules_spring` Bazel 9
  external Java-rule autoload option under `common` so `query`/`cquery` work.
- `.bazel_downloader_config` stores URL rewrite rules for downloads made by
  Bazel repository rules. For example, it can redirect `repo1.maven.org` to a
  reliable Maven Central mirror. It does not select dependency versions, does
  not replace repositories declared in `MODULE.bazel`, and must not contain
  credentials.
- `MODULE.bazel` is the developer-edited Bzlmod and third-party dependency
  input. It is not a POM.
- `maven_install.json` is the generated exact lock for Java artifacts resolved
  from Maven coordinates. Its name does not mean Bazel publishes Maven-shaped
  project artifacts.
- `MODULE.bazel.lock` is Bazel's generated lock for modules and extensions.
  Do not hand-edit either lockfile; commit changed inputs and generated locks
  together.

When the repository needs its first `maven_install.json`, explain the tested
bootstrap in plain Maven-user terms rather than saying only "run pin." With
`rules_jvm_external` 7.0, the app starts with an exported zero-byte owner lock
that the pin target replaces. If a source dependency contributes the same
shared hub with its own lock, explain the two stages explicitly:

1. Temporarily hide that source contributor and pin the app's dependencies to
   create the app-owned lock.
2. Restore the contributor and pin again to create the complete shared lock.

If development used `--override_module` to point at a local checkout, require
one final pin without it. Explain that a build proves remote source compiles,
while this final pin proves another developer or CI can regenerate the lock
from the pinned Git repository. Never recommend copying another repository's
lockfile.

## Required Command Sections

Set one portable output root and reuse it in every example:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/<app>-bazel-cache"
```

Include real commands for all of these workflows:

1. Clean with `bazel --output_user_root=... clean`; describe `--expunge` as an
   exceptional cache reset, not a normal prerequisite.
2. Build everything and build each principal module/product target.
3. Test everything uncached and test each module independently.
4. Run one test class while ConsoleLauncher scans the test jar. Use a class
   name filter, not an additional explicit selector:

   ```bash
   bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
     test //<module>:tests \
     --cache_test_results=no \
     --test_output=streamed \
     --test_arg=--exclude-classname='^(?!<fully-qualified-test-class>$).*$'
   ```

5. Run one test method by combining the class filter with a method-name filter:

   ```bash
   --test_arg=--include-methodname='.*#<method-name>$'
   ```

6. Locate `test.log`, the real ConsoleLauncher report at
   `test.outputs/junit/TEST-junit-jupiter.xml`, `jacoco.exec`, `jacoco.xml`,
   and the JaCoCo HTML `index.html` under the target's `bazel-testlogs/...`
   directory. Explain that the nearby outer `test.xml` contains one Bazel
   shell-wrapper testcase and must not be used as the GitLab JUnit report.
   Include the Sonar XML property or command used by the repository.
7. Build and inspect the executable `app.jar`, including manifest and required
   metadata entries.
8. Generate and check NOTICE when the OSS distribution contract requires it;
   omit the entire section for managed applications.
9. Build the Docker image from the real `bazel info bazel-bin` path and show a
   complete `docker run` command for the intended local profile, ports, service
   addresses, network, and bind mounts. Do not write only "run it like Maven."
10. Show `--override_module=<module>=<absolute-path>` as a temporary local
    validation option and warn never to commit `local_path_override` or a
    workstation path.
11. List every opt-in GitLab variable in a small table, with its default,
    purpose, and which jobs it enables.
12. Explain first-time pinning and normal repinning commands.

## Documentation Completion Gate

Verify every command against the generated target names before finalizing.
Also create and update:

```text
bazel-enablement/roadmap.md
bazel-enablement/status.md
bazel-enablement/handoff.md
```

The status and handoff must record exact validation evidence and explicit
CI-only or registry deferrals. Do not claim completion because `BAZEL.md`
exists while these workflows or documents are missing.
