# NV Boot BOM

Bill of Materials (BOM) for `nv-boot-starter-*` module versions.

## Preferred usage: extend nv-boot-parent

Most applications should extend `nv-boot-parent` and import this BOM explicitly in
`<dependencyManagement>`. See the [nv-boot-parent README](../README.md) for full details.

## Alternative usage: import as a BOM only

If your project already has a parent POM it cannot change, import `nv-boot-bom` directly in
your `<dependencyManagement>` block to get managed versions for all `nv-boot-starter-*` modules:

```xml
<dependencyManagement>
    <dependencies>
        <dependency>
            <groupId>com.nvidia.boot</groupId>
            <artifactId>nv-boot-bom</artifactId>
            <version>${get.latest.version}</version>
            <type>pom</type>
            <scope>import</scope>
        </dependency>
    </dependencies>
</dependencyManagement>
```

Once imported, add individual nv-boot libraries without specifying versions:

```xml
<dependencies>
    <dependency>
        <groupId>com.nvidia.boot</groupId>
        <artifactId>nv-boot-starter-jwt</artifactId>
    </dependency>
    <dependency>
        <groupId>com.nvidia.boot</groupId>
        <artifactId>nv-boot-starter-registries</artifactId>
    </dependency>
    <!-- etc. -->
</dependencies>
```

Note that importing this BOM does **not** provide managed versions for the third-party dependencies
used internally by nv-boot libraries (e.g. Bouncy Castle, Nimbus JOSE+JWT, Spring Cloud). Those
are managed in `nv-boot-parent` and are only available to projects that extend it. If you use
any of those libraries directly, you will need to declare their versions explicitly.

## No Beans To Inject

This is a BOM (Bill of Materials) module only. It does not provide any beans or auto-configuration.
It only manages dependency versions for `nv-boot-starter-*` modules.

