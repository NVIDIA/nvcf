#!/usr/bin/env bash
set -euo pipefail

find_workspace_above() {
    local candidate="$1"
    while [[ "${candidate}" != "/" ]]; do
        if [[ -f "${candidate}/MODULE.bazel" ]]; then
            printf '%s\n' "${candidate}"
            return
        fi
        candidate="$(dirname "${candidate}")"
    done
    return 1
}

find_workspace() {
    if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" && -f "${BUILD_WORKSPACE_DIRECTORY}/MODULE.bazel" ]]; then
        printf '%s\n' "${BUILD_WORKSPACE_DIRECTORY}"
        return
    fi

    local candidate
    for runfiles_root in "${TEST_SRCDIR:-}" "${RUNFILES_DIR:-}"; do
        if [[ -z "${runfiles_root}" ]]; then
            continue
        fi
        for candidate in "${runfiles_root}/_main" "${runfiles_root}/nv_boot_parent"; do
            if [[ -f "${candidate}/MODULE.bazel" ]]; then
                printf '%s\n' "${candidate}"
                return
            fi
        done
    done

    local script_dir
    script_dir="$(cd "$(dirname "$0")" && pwd)"

    local workspace
    if workspace="$(find_workspace_above "${script_dir}")"; then
        printf '%s\n' "${workspace}"
        return
    fi

    printf 'Could not find MODULE.bazel in test runfiles\n' >&2
    exit 1
}

workspace="$(find_workspace)"
# nv-boot-parent is its own standalone Bazel module: its module root is the
# nv-boot-parent directory, so the notice tooling, its NOTICE, and its
# maven_install.json all live at the workspace root.
nvboot="${workspace}"

PYTHONPATH="${nvboot}/tools/bazel" python3 - <<'PY'
import pathlib
import tempfile
import zipfile

from generate_notice import dependency_closure, parse_pom_xml, runtime_jar_coordinates

parse_pom_xml("<project><name>safe</name></project>")
for unsafe_xml in (
    '<!DOCTYPE project SYSTEM "https://example.invalid/pom.dtd"><project/>',
    '<!DOCTYPE project [<!ENTITY name "unsafe">]><project>&name;</project>',
):
    try:
        parse_pom_xml(unsafe_xml)
    except ValueError:
        continue
    raise AssertionError("POM parser accepted a DTD or entity declaration")

try:
    dependency_closure(
        ["example:missing:1.0"],
        {"artifacts": {}, "dependencies": {}},
        (),
    )
except ValueError:
    pass
else:
    raise AssertionError("NOTICE closure silently accepted a missing dependency root")

with tempfile.TemporaryDirectory() as directory:
    app_jar = pathlib.Path(directory, "app.jar")
    with zipfile.ZipFile(app_jar, "w") as archive:
        archive.writestr(
            "BOOT-INF/lib/external/rules_jvm_external++maven+nv_third_party_deps/"
            "com/example/demo/1.0/demo-1.0.jar",
            b"",
        )
    try:
        runtime_jar_coordinates(
            [app_jar],
            {"artifacts": {"com.example:demo": {"version": "2.0"}}},
            (),
        )
    except ValueError:
        pass
    else:
        raise AssertionError("Runtime NOTICE accepted a stale lockfile version")
PY

exec python3 "${nvboot}/tools/bazel/generate_notice.py" \
    --maven-install "${workspace}/maven_install.json" \
    --metadata "${nvboot}/tools/bazel/notice_metadata.json" \
    --notice "${nvboot}/NOTICE" \
    --root-manifest "${nvboot}/tools/bazel/notice_roots.json" \
    --check
