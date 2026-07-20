# Spring Boot Application Profiles

Classify the target repository before creating build or CI targets. Product
names are not identities: an open-source/self-hosted service and its managed
counterpart can be separate applications with different source topology and
release behavior.

This file describes shapes, not dependencies. Never add a module from a prior
example unless the target POM/source/CI independently proves that dependency.

## Open-Source Or Self-Hosted Reactor

Typical characteristics:

- an aggregator repository with core/library and executable-app modules;
- source targets for the internal modules;
- runtime NOTICE when required for distribution;
- an application jar and container as final products;
- no assumption that managed-only CI publication behavior applies.

Earlier migrations validated this shape with an in-repository core module and
an executable service. Their product names are deliberately omitted here so an
agent cannot mistake the example for a required dependency.

## Internal Managed Application

Typical characteristics:

- a separate repository whose root is the executable Spring Boot app;
- no aggregator or in-repository core module;
- source dependencies on shared core, nv-boot, and managed nv-boot modules;
- no NOTICE; this is part of the managed profile and requires no separate user
  confirmation;
- the same executable app, test, coverage, Docker, and CI responsibilities as
  any other Spring Boot application.

Earlier managed migrations validated this shape with source dependencies on
shared library modules outside the application repository. Discover those
modules from the target repository; do not copy them from a prior service.

For this profile, do not invent a reusable application library artifact. A
private `app_classes` target may compile sources for tests and rules_spring,
but the product target is the executable `app.jar` and then the container.

Managed apps commonly need two kinds of test input at the same time: config
checked into the managed repository at an expected relative path and shared
integration assets from a source-built core fixture target. Declare both as
runfiles. Merely adding the external fixture target does not place its files at
the managed workspace's root-relative path.

## Optional Managed OpenAPI Publication

Some managed apps generate OpenAPI specifications after merge to the default
branch, commit or push those files to a documentation repository, and create a
merge request. Other managed apps do not. Preserve this as an optional existing
capability, not a default feature of every managed app.

During discovery, inspect both local and inherited GitLab configuration for:

- the enablement flag and branch rules;
- output filenames and application variants;
- destination project/repository identifiers;
- runtime profiles, feature flags, ports, and readiness checks;
- databases, containers, test fixtures, and secrets needed to start the app;
- artifact dependencies between build, generation, and downstream publication
  jobs;
- credentials and rules used for commit, push, and merge-request creation.

During Maven/Bazel coexistence, leave Maven as the default. Add an opt-in Bazel
validation path that downloads the archived Bazel-built `app.jar` and supplies
it to spec generation without changing the generated filenames or downstream
publication contract. If an existing job hardcodes `target/app.jar`, either
stage the Bazel artifact there in the Bazel-specific job or parameterize the jar
path without changing the Maven default.

Keep the existing OpenAPI generation job name when downstream jobs name it in
`needs`. Make Maven and Bazel build artifacts optional inputs and choose one in
the generation job according to an explicit flag. If the job validates
`NEXT_VERSION`, add the version-computation job as a direct need; dotenv values
are not a transitive artifact contract.

Before disabling Maven, prove all of the following in CI:

1. The Bazel-built app starts with every required fixture and profile.
2. Every expected OpenAPI variant is generated under the established filename.
3. The downstream job consumes those exact files.
4. The documentation-repository commit/push and merge-request workflow
   completes under its existing branch and credential rules.

Do not add OpenAPI jobs to an app that does not already have this requirement.
