"""Shared NOTICE generation rules for Java components in the NVCF monorepo."""

load("@rules_shell//shell:sh_test.bzl", _sh_test = "sh_test")

_DIFF_TEST = "//tools/bazel/java:notice_diff_test.sh"
_GENERATOR = "//tools/bazel/java:generate_notice_tool"
_LICENSE_ALIASES = "//tools/bazel/java:license_aliases.json"
_MAVEN_INSTALL = "//:maven_install.json"


def _workspace_path(label):
    if label.startswith(":"):
        return native.package_name() + "/" + label[1:]
    if label.startswith("//"):
        package_and_name = label[2:].split(":")
        if len(package_and_name) != 2:
            fail("Expected an explicit //package:file label, got %s" % label)
        return package_and_name[0] + "/" + package_and_name[1]
    fail("Expected a local or absolute source-file label, got %s" % label)


def _action_arguments(
        metadata,
        shared_metadata,
        root_manifests,
        runtime_target,
        first_party_groups):
    arguments = [
        '--maven-install "$(location %s)"' % _MAVEN_INSTALL,
        '--metadata "$(location %s)"' % metadata,
        '--license-aliases "$(location %s)"' % _LICENSE_ALIASES,
    ]
    for label in shared_metadata:
        arguments.append('--shared-metadata "$(location %s)"' % label)
    for label in root_manifests:
        arguments.append('--root-manifest "$(location %s)"' % label)
    if runtime_target:
        arguments.append('--runtime-jar "$(location %s)"' % runtime_target)
    for group in first_party_groups:
        arguments.append("--first-party-group %s" % group)
    return arguments


def _update_arguments(
        checked_notice,
        metadata,
        shared_metadata,
        root_manifests,
        runtime_target,
        first_party_groups):
    arguments = [
        '--maven-install "$${workspace}/maven_install.json"',
        '--metadata "$${workspace}/%s"' % _workspace_path(metadata),
        '--notice "$${workspace}/%s"' % _workspace_path(checked_notice),
        '--license-aliases "$${workspace}/tools/bazel/java/license_aliases.json"',
    ]
    for label in shared_metadata:
        arguments.append(
            '--shared-metadata "$${workspace}/%s"' % _workspace_path(label)
        )
    for label in root_manifests:
        arguments.append(
            '--root-manifest "$${workspace}/%s"' % _workspace_path(label)
        )
    if runtime_target:
        arguments.append('--runtime-jar "$(location %s)"' % runtime_target)
    for group in first_party_groups:
        arguments.append("--first-party-group %s" % group)
    return arguments


def nvcf_notice(
        checked_notice,
        metadata,
        shared_metadata = [],
        root_manifests = [],
        runtime_target = None,
        first_party_groups = [],
        name = "third_party_notice",
        inventory_name = "runtime_inventory"):
    """Generates and checks one component's complete third-party NOTICE."""
    if bool(root_manifests) == bool(runtime_target):
        fail("Set exactly one of root_manifests or runtime_target")

    srcs = [
        _LICENSE_ALIASES,
        _MAVEN_INSTALL,
        checked_notice,
        metadata,
    ] + shared_metadata + root_manifests
    if runtime_target:
        srcs.append(runtime_target)

    action_arguments = _action_arguments(
        metadata,
        shared_metadata,
        root_manifests,
        runtime_target,
        first_party_groups,
    )
    native.genrule(
        name = name,
        srcs = srcs,
        outs = [
            "THIRD_PARTY_NOTICE",
            "runtime_inventory.json",
        ],
        cmd = """
set -eu
"$(execpath {generator})" \
    {arguments} \
    --output "$(@D)/THIRD_PARTY_NOTICE" \
    --inventory-output "$(@D)/runtime_inventory.json" \
    --write
""".format(
            arguments = " \\\n    ".join(action_arguments),
            generator = _GENERATOR,
        ),
        tools = [_GENERATOR],
    )

    native.filegroup(
        name = inventory_name,
        srcs = [":runtime_inventory.json"],
    )

    _sh_test(
        name = "notice_check_test",
        srcs = [_DIFF_TEST],
        args = [
            "$(location :THIRD_PARTY_NOTICE)",
            "$(location %s)" % checked_notice,
        ],
        data = [
            ":THIRD_PARTY_NOTICE",
            checked_notice,
        ],
        size = "small",
    )

    update_arguments = _update_arguments(
        checked_notice,
        metadata,
        shared_metadata,
        root_manifests,
        runtime_target,
        first_party_groups,
    )
    native.genrule(
        name = "generate_notice",
        srcs = srcs,
        outs = ["generate_notice.sh"],
        cmd = """
cat > "$@" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

workspace="$${{BUILD_WORKSPACE_DIRECTORY:?Run this command through bazel run}}"
exec "$(execpath {generator})" \
    {arguments} \
    "$$@"
EOF
chmod +x "$@"
""".format(
            arguments = " \\\n    ".join(update_arguments),
            generator = _GENERATOR,
        ),
        executable = True,
        tools = [_GENERATOR],
    )


def nvcf_notice_delta(
        inventory,
        baseline_inventories,
        name = "osrb_dependency_delta"):
    """Generates machine-readable and license-grouped OSRB dependency deltas."""
    baseline_arguments = [
        '--baseline-inventory "$(location %s)"' % label
        for label in baseline_inventories
    ]
    native.genrule(
        name = name,
        srcs = [inventory] + baseline_inventories,
        outs = [
            name + ".json",
            name + ".md",
        ],
        cmd = """
set -eu
"$(execpath {generator})" \
    --delta-inventory "$(location {inventory})" \
    {baselines} \
    --delta-json-output "$(@D)/{name}.json" \
    --delta-markdown-output "$(@D)/{name}.md"
""".format(
            baselines = " \\\n    ".join(baseline_arguments),
            generator = _GENERATOR,
            inventory = inventory,
            name = name,
        ),
        tools = [_GENERATOR],
    )
