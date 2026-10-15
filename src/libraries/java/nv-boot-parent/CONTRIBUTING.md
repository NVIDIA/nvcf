# Contributing to `nv-boot-parent`

Thank you for your interest in contributing to this project. We welcome
contributions from the community. Please read these guidelines before
submitting a pull request.

## Developer Certificate of Origin (DCO)

All contributions must be signed off with the Developer Certificate of Origin.
By adding a `Signed-off-by` line to your commits you certify that you have the
right to submit the contribution under the project license.

```bash
git commit -s -m "feat: your commit message"
```

This adds a line like:

```
Signed-off-by: Your Name <your.email@example.com>
```

## How to Contribute

1. **Fork** the repository and create your branch from `main`.
2. **Make your changes** and add tests where applicable.
3. **Ensure tests pass** (for example `mvn verify` or `mvn test`).
4. **Sign your commits** with `git commit -s`.
5. **Submit a pull request** with a clear description of the change.

## Reporting Issues

Please use the project’s issue tracker to report bugs or request features.
Include as much detail as possible.

## Java code style

This document complements the project’s Maven configuration (compiler settings,
Checkstyle, Spotless, etc.) when those tools are enabled.

### Copyright and license headers

- **Java sources** must use the block comment (`/* … */`) copyright header prescribed by NVIDIA Legal for Apache 2.0. See the [Apache 2.0](https://nvidia.atlassian.net/wiki/spaces/LEG/pages/2417590704/Apache+2.0) Confluence page for the authoritative text.
- **`.properties` files** must use the `#`-prefixed header from the same page.
- **`pom.xml` files** must use XML comments (`<!-- … -->`) with the same legal text, placed immediately after the `<?xml …?>` declaration when present.
- **`META-INF/spring/*.imports`** files (including `org.springframework.boot.autoconfigure.AutoConfiguration.imports`) must use the `#`-prefixed header from the same page.
- If Legal updates the template, apply the new wording project-wide (new and touched files at minimum).

### Language and platform

- Target the **Java** and **Spring Boot** versions declared in the root POM. Prefer APIs available on that baseline; avoid preview features unless the build explicitly enables them and the team agrees.

### Style and formatting

- Follow the formatting rules enforced by the build (e.g. Spotless, formatter plugin). When in doubt, match existing modules in this repo.
- Use **4 spaces** for indentation in Java unless the formatter dictates otherwise.
- Prefer **explicit imports** over star imports; keep imports ordered consistently with the rest of the tree.
- **Line length**: stay within the limit enforced by Checkstyle/Spotless (or ~100–120 columns if no tool is configured), breaking only where readability improves.

### Naming and structure

- **Packages**: lowercase, no underscores; align with existing `com.nvidia.nv.boot.*` layout.
- **Classes**: `PascalCase`; methods and fields: `camelCase`; constants: `UPPER_SNAKE_CASE`.
- One top-level **public** class per file, named after the file.
- Keep classes **focused**: prefer small types and composition over large “god” classes.

### Spring Boot and dependency injection

- Prefer **constructor injection** for required collaborators; use `@Autowired` on the constructor when the class is a Spring-managed bean with multiple dependencies.
- Avoid field injection for mandatory dependencies unless there is a documented reason (e.g. framework limitations).
- Configuration properties should use typed `@ConfigurationProperties` where appropriate, with validation (`@Validated`, `@NotNull`, etc.) when values are required.

### APIs and visibility

- Minimize **public** surface area: prefer package-private or internal types until an API is intentionally stable.
- Use **Javadoc** on public types and non-obvious public methods—describe behavior, parameters, return values, and thrown exceptions, not redundant restatements of the method name.

### Error handling and logging

- Throw **specific, meaningful** exceptions; avoid bare `Exception` or `RuntimeException` for domain errors when a dedicated type exists or should exist.
- Log at **appropriate levels** (`error` for failures requiring attention, `warn` for recoverable issues, `debug`/`trace` for diagnostics). Do not log secrets or full payloads that may contain PII unless required and approved.

### Testing

- Add or update **unit tests** for behavior changes; use the same testing stack as sibling modules (JUnit 5, Mockito, Spring Test, etc.).
- Prefer **readable** test names (`methodConditionExpected`) and **Arrange–Act–Assert** structure.

### Security and robustness

- Do not hardcode **secrets**; use configuration or secret management appropriate to the deployment environment.
- Validate **external input** at boundaries (HTTP, messaging, file uploads).
- Be careful with **serialization** and reflection on user-controlled data; follow Spring and Jackson safe defaults.

### Pull requests

- Keep changes **scoped** to the task; avoid unrelated refactors in the same PR.
- Update **documentation** and **tests** when behavior or public APIs change.

## License

By contributing to this project, you agree that your contributions will be
licensed under the [Apache License 2.0](LICENSE).
