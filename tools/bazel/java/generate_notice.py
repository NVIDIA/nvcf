#!/usr/bin/env python3
"""Generates the third-party NOTICE file from Bazel dependency metadata.

The build/test path is hermetic: it reads `maven_install.json`, checked-in
component metadata, and either declared library roots or a packaged runtime
jar. Only the explicit metadata-update path reads dependency POMs from a local
Maven cache or remote Maven-compatible repository.
"""

import argparse
import difflib
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
import zipfile


MAVEN_NS = {"m": "http://maven.apache.org/POM/4.0.0"}
PROJECT_VERSION_PATTERN = re.compile(r"\$\{([^}]+)\}")
NOTICE_LINE_PATTERN = re.compile(
    r"\((?P<coordinate>[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+:[^()\s]+) - (?P<url>[^)]*)\)\s*$"
)
UNSAFE_XML_DECLARATION_PATTERN = re.compile(
    r"<!\s*(?:DOCTYPE|ENTITY)\b",
    re.IGNORECASE,
)

FIRST_PARTY_GROUPS = ("com.nvidia.boot",)
EXCLUDED_SCOPES = {"test", "provided"}


def parse_pom_xml(text):
    """Parses POM XML after rejecting declarations Maven POMs do not need."""
    if UNSAFE_XML_DECLARATION_PATTERN.search(text):
        raise ValueError("POM XML must not contain DTD or entity declarations")
    return ET.fromstring(text)


def parse_dependency_coordinate(value):
    parts = value.split(":")
    if len(parts) < 2 or len(parts) > 5:
        raise ValueError(f"Invalid dependency coordinate: {value}")

    return {
        "group_id": parts[0],
        "artifact_id": parts[1],
        "version": parts[2] if len(parts) >= 3 else "",
        "scope": parts[3] if len(parts) >= 4 and parts[3] else "compile",
        "optional": parts[4].lower() == "true" if len(parts) >= 5 else False,
    }


def coordinate_key(group_id, artifact_id):
    return f"{group_id}:{artifact_id}"


def versioned_coordinate(group_id, artifact_id, version):
    return f"{group_id}:{artifact_id}:{version}"


def load_json(path):
    return json.loads(path.read_text())


def metadata_artifacts(metadata):
    return metadata.get("artifacts", {})


def merge_metadata(shared_documents, primary_document, reject_primary_overlap=True):
    """Merges shared metadata with component-owned metadata."""
    merged = {}
    for source, document in shared_documents:
        for coordinate, entry in metadata_artifacts(document).items():
            existing = merged.get(coordinate)
            if existing is not None and existing != entry:
                raise ValueError(
                    f"Conflicting shared NOTICE metadata for {coordinate}: {source}"
                )
            merged[coordinate] = entry

    for coordinate, entry in metadata_artifacts(primary_document).items():
        existing = merged.get(coordinate)
        if existing is not None:
            if existing != entry:
                raise ValueError(
                    f"Component NOTICE metadata conflicts with shared metadata for {coordinate}"
                )
            if reject_primary_overlap:
                raise ValueError(
                    f"Component NOTICE metadata duplicates shared metadata for {coordinate}"
                )
        merged[coordinate] = entry

    return {"artifacts": merged}


def prune_shared_metadata(primary_document, shared_documents):
    """Removes exact shared entries from component-owned metadata."""
    shared = merge_metadata(shared_documents, {"artifacts": {}}, False)
    artifacts = {}
    for coordinate, entry in metadata_artifacts(primary_document).items():
        shared_entry = metadata_artifacts(shared).get(coordinate)
        if shared_entry is not None:
            if shared_entry != entry:
                raise ValueError(
                    f"Component NOTICE metadata conflicts with shared metadata for {coordinate}"
                )
            continue
        artifacts[coordinate] = entry
    return {
        "generated_by": primary_document.get(
            "generated_by",
            "tools/bazel/java/generate_notice.py",
        ),
        "artifacts": {key: artifacts[key] for key in sorted(artifacts)},
    }


def load_license_aliases(path):
    if not path:
        return {}
    return load_json(path).get("aliases", {})


def normalized_licenses(licenses, aliases):
    return sorted({aliases.get(license_name, license_name) for license_name in licenses})


def load_root_manifests(paths):
    roots = []
    for path in paths:
        data = load_json(path)
        roots.extend(data.get("dependencies", []))
    return roots


def is_first_party(group_id, first_party_groups):
    return any(
        group_id == prefix or group_id.startswith(f"{prefix}.")
        for prefix in first_party_groups
    )


def should_include_root(dep, first_party_groups):
    if is_first_party(dep["group_id"], first_party_groups):
        return False
    return dep["scope"] not in EXCLUDED_SCOPES


def resolved_version(maven_install, group_id, artifact_id):
    artifact = maven_install["artifacts"].get(coordinate_key(group_id, artifact_id))
    if not artifact:
        return ""
    return artifact.get("version", "")


def dependency_closure(root_dependencies, maven_install, first_party_groups):
    included = set()
    queue = []

    for coordinate in root_dependencies:
        dep = parse_dependency_coordinate(coordinate)
        if not should_include_root(dep, first_party_groups):
            continue
        version = dep["version"]
        if version.startswith("${") or not version:
            version = resolved_version(maven_install, dep["group_id"], dep["artifact_id"])
        if not version:
            raise ValueError(f"Could not resolve version for NOTICE root {coordinate}")
        key = coordinate_key(dep["group_id"], dep["artifact_id"])
        if key not in maven_install["artifacts"]:
            raise ValueError(f"NOTICE root {coordinate} is missing from maven_install.json")
        queue.append(key)

    while queue:
        key = queue.pop(0)
        if key in included:
            continue
        group_id, artifact_id = key.split(":", 1)
        if is_first_party(group_id, first_party_groups):
            continue
        artifact = maven_install["artifacts"].get(key)
        if not artifact:
            continue
        included.add(key)
        for child in maven_install.get("dependencies", {}).get(key, []):
            if child not in included:
                queue.append(child)

    return sorted(included)


def runtime_jar_coordinates(paths, maven_install, first_party_groups):
    """Returns Maven coordinates for rules_jvm_external jars shipped in app jars."""
    coordinates = set()
    prefix = "BOOT-INF/lib/external/rules_jvm_external++maven+"

    for path in paths:
        with zipfile.ZipFile(path) as app_jar:
            for entry in app_jar.namelist():
                if not entry.startswith(prefix) or not entry.endswith(".jar"):
                    continue

                # Repository name / group path / artifact / version / jar.
                relative = entry[len(prefix):]
                parts = relative.split("/")
                if len(parts) < 5:
                    continue
                coordinate_parts = parts[1:]
                group_id = ".".join(coordinate_parts[:-3])
                artifact_id = coordinate_parts[-3]
                version = coordinate_parts[-2]
                if not group_id or is_first_party(group_id, first_party_groups):
                    continue

                key = coordinate_key(group_id, artifact_id)
                artifact = maven_install.get("artifacts", {}).get(key)
                if artifact is None:
                    raise ValueError(
                        f"Runtime jar coordinate {key} from {entry} is missing "
                        "from maven_install.json"
                    )
                if artifact["version"] != version:
                    raise ValueError(
                        f"Runtime jar {entry} ships {version}, but "
                        f"maven_install.json pins {artifact['version']} for {key}"
                    )
                coordinates.add(key)

    return sorted(coordinates)


def generated_notice(coordinates, maven_install, metadata):
    lines = ["", f"Lists of {len(coordinates)} third-party dependencies."]
    missing = []
    incomplete = []
    for key in coordinates:
        artifact = maven_install["artifacts"].get(key)
        if not artifact:
            continue
        group_id, artifact_id = key.split(":", 1)
        version = artifact["version"]
        versioned = versioned_coordinate(group_id, artifact_id, version)
        entry = metadata.get("artifacts", {}).get(versioned)
        if not entry:
            missing.append(versioned)
            continue
        licenses = entry.get("licenses") or []
        if not licenses or "UNKNOWN" in licenses:
            incomplete.append(versioned)
            continue
        license_text = " ".join(f"({license_name})" for license_name in licenses)
        name = entry.get("name") or artifact_id
        url = entry.get("url") or ""
        lines.append(f"     {license_text} {name} ({versioned} - {url})")

    if missing:
        raise ValueError(
            "Missing NOTICE metadata for:\n"
            + "\n".join(f"  {coordinate}" for coordinate in missing)
            + "\nRun the component's Bazel generate_notice target with "
            "--update-metadata --write"
        )
    if incomplete:
        raise ValueError(
            "Incomplete NOTICE metadata for:\n"
            + "\n".join(f"  {coordinate}" for coordinate in incomplete)
            + "\nFix the component notice_metadata.json or rerun metadata "
            "generation after POM metadata is available."
        )
    lines.append("")
    return "\n".join(lines)


def generated_inventory(coordinates, maven_install, metadata, aliases):
    dependencies = []
    for key in coordinates:
        artifact = maven_install["artifacts"].get(key)
        if not artifact:
            continue
        group_id, artifact_id = key.split(":", 1)
        coordinate = versioned_coordinate(
            group_id,
            artifact_id,
            artifact["version"],
        )
        entry = metadata_artifacts(metadata).get(coordinate)
        if not entry:
            raise ValueError(f"Missing NOTICE metadata for {coordinate}")
        dependencies.append(
            {
                "coordinate": coordinate,
                "licenses": normalized_licenses(entry.get("licenses", []), aliases),
                "name": entry.get("name") or artifact_id,
                "url": entry.get("url") or "",
            }
        )
    return {
        "generated_by": "tools/bazel/java/generate_notice.py",
        "dependencies": dependencies,
    }


def inventory_coordinates(inventory):
    return {
        dependency["coordinate"]
        for dependency in inventory.get("dependencies", [])
    }


def generated_delta(inventory, baseline_inventories):
    baseline_coordinates = set()
    for baseline in baseline_inventories:
        baseline_coordinates.update(inventory_coordinates(baseline))

    dependencies = [
        dependency
        for dependency in inventory.get("dependencies", [])
        if dependency["coordinate"] not in baseline_coordinates
    ]
    dependencies.sort(key=lambda dependency: dependency["coordinate"])

    groups = {}
    for dependency in dependencies:
        licenses = dependency.get("licenses") or ["UNKNOWN"]
        group = " / ".join(sorted(licenses))
        groups.setdefault(group, []).append(dependency)

    return {
        "generated_by": "tools/bazel/java/generate_notice.py",
        "dependencies": dependencies,
        "groups": {key: groups[key] for key in sorted(groups)},
    }


def generated_delta_markdown(delta):
    dependencies = delta["dependencies"]
    lines = [
        "# OSRB Dependency Delta",
        "",
        (
            f"{len(dependencies)} new or additional third-party "
            "dependencies require review."
        ),
        "",
    ]
    if not dependencies:
        lines.extend(["No new or additional dependencies were found.", ""])
        return "\n".join(lines)

    for license_group, entries in delta["groups"].items():
        lines.extend([f"## {license_group}", ""])
        for entry in entries:
            suffix = f" - {entry['url']}" if entry.get("url") else ""
            lines.append(
                f"- `{entry['coordinate']}` - {entry['name']}{suffix}"
            )
        lines.append("")
    return "\n".join(lines)


def compare_or_write(expected, notice_path, write):
    if write:
        notice_path.write_text(expected)
        return ""
    actual = notice_path.read_text() if notice_path.exists() else ""
    if actual == expected:
        return ""
    return "".join(
        difflib.unified_diff(
            actual.splitlines(True),
            expected.splitlines(True),
            fromfile=str(notice_path),
            tofile=f"{notice_path} (generated)",
        )
    )


def maven_path_for(group_id, artifact_id, version, extension="pom"):
    group_path = group_id.replace(".", "/")
    return f"{group_path}/{artifact_id}/{version}/{artifact_id}-{version}.{extension}"


def local_maven_repositories():
    repos = []
    local_repo = os.environ.get("MAVEN_REPO")
    if local_repo:
        repos.append(pathlib.Path(local_repo).expanduser())
    repos.append(pathlib.Path.home() / ".m2" / "repository")
    return repos


def find_local_pom(group_id, artifact_id, version):
    relative = pathlib.Path(maven_path_for(group_id, artifact_id, version))
    for repo in local_maven_repositories():
        candidate = repo / relative
        if candidate.exists():
            return candidate
    return None


def repository_urls(maven_install):
    return [repo.rstrip("/") for repo in maven_install.get("repositories", []) if repo.startswith("http")]


def fetch_remote_pom(group_id, artifact_id, version, repositories):
    relative = maven_path_for(group_id, artifact_id, version)
    errors = []
    for repo in repositories:
        url = f"{repo}/{relative}"
        try:
            with urllib.request.urlopen(url, timeout=30) as response:
                return response.read().decode("utf-8")
        except (urllib.error.URLError, TimeoutError) as error:
            errors.append(f"{url}: {error}")
    raise FileNotFoundError(
        f"Could not find POM for {group_id}:{artifact_id}:{version}\n" + "\n".join(errors)
    )


def xml_find(element, path):
    found = element.find(path, MAVEN_NS)
    if found is not None:
        return found
    return element.find(path.replace("m:", ""))


def xml_findall(element, path):
    found = element.findall(path, MAVEN_NS)
    if found:
        return found
    return element.findall(path.replace("m:", ""))


def xml_text(element, path):
    found = xml_find(element, path)
    if found is None or found.text is None:
        return ""
    return found.text.strip()


def pom_parent(root):
    parent = xml_find(root, "m:parent")
    if parent is None:
        return None
    group_id = xml_text(parent, "m:groupId")
    artifact_id = xml_text(parent, "m:artifactId")
    version = xml_text(parent, "m:version")
    if not group_id or not artifact_id or not version:
        return None
    return group_id, artifact_id, version


def pom_properties(root, group_id, artifact_id, version, inherited=None):
    properties = dict(inherited or {})
    properties.update({
        "project.groupId": group_id,
        "project.artifactId": artifact_id,
        "project.version": version,
        "pom.groupId": group_id,
        "pom.artifactId": artifact_id,
        "pom.version": version,
    })
    properties_node = xml_find(root, "m:properties")
    if properties_node is not None:
        for child in list(properties_node):
            name = child.tag.rsplit("}", 1)[-1]
            properties[name] = (child.text or "").strip()
    return properties


def substitute_properties(value, properties):
    if not value:
        return ""

    def replace(match):
        return properties.get(match.group(1), match.group(0))

    return PROJECT_VERSION_PATTERN.sub(replace, value)


class PomMetadataResolver:
    def __init__(self, maven_install):
        self.maven_install = maven_install
        self.repositories = repository_urls(maven_install)
        self.cache = {}

    def pom_text(self, group_id, artifact_id, version):
        local = find_local_pom(group_id, artifact_id, version)
        if local:
            return local.read_text()
        return fetch_remote_pom(group_id, artifact_id, version, self.repositories)

    def resolve(self, group_id, artifact_id, version):
        key = versioned_coordinate(group_id, artifact_id, version)
        if key in self.cache:
            return self.cache[key]

        text = self.pom_text(group_id, artifact_id, version)
        root = parse_pom_xml(text)
        parent_coordinate = pom_parent(root)
        parent = {}
        inherited_properties = {}
        if parent_coordinate:
            parent = self.resolve(*parent_coordinate)
            inherited_properties = parent.get("properties", {})

        properties = pom_properties(root, group_id, artifact_id, version, inherited_properties)
        name = substitute_properties(xml_text(root, "m:name"), properties) or parent.get("name") or artifact_id
        url = substitute_properties(xml_text(root, "m:url"), properties) or parent.get("url") or ""

        licenses = []
        licenses_node = xml_find(root, "m:licenses")
        if licenses_node is not None:
            for license_node in xml_findall(licenses_node, "m:license"):
                license_name = substitute_properties(xml_text(license_node, "m:name"), properties)
                if license_name:
                    licenses.append(license_name)
        if not licenses:
            licenses = list(parent.get("licenses", []))

        result = {
            "name": name,
            "url": url,
            "licenses": licenses or ["UNKNOWN"],
            "properties": properties,
        }
        self.cache[key] = result
        return result


def update_metadata(
    coordinates,
    maven_install,
    existing_metadata,
    shared_metadata=None,
):
    resolver = PomMetadataResolver(maven_install)
    artifacts = dict(existing_metadata.get("artifacts", {}))
    shared_artifacts = metadata_artifacts(shared_metadata or {})
    for key in coordinates:
        artifact = maven_install["artifacts"].get(key)
        if not artifact:
            continue
        group_id, artifact_id = key.split(":", 1)
        version = artifact["version"]
        versioned = versioned_coordinate(group_id, artifact_id, version)
        if versioned in shared_artifacts:
            continue
        resolved = resolver.resolve(group_id, artifact_id, version)
        artifacts[versioned] = {
            "licenses": resolved["licenses"],
            "name": resolved["name"],
            "url": resolved["url"],
        }
    return {
        "generated_by": "tools/bazel/java/generate_notice.py --update-metadata",
        "artifacts": {key: artifacts[key] for key in sorted(artifacts)},
    }


def current_notice_coordinates(path):
    if not path.exists():
        return set()
    coordinates = set()
    for line in path.read_text().splitlines():
        match = NOTICE_LINE_PATTERN.search(line)
        if match:
            coordinates.add(match.group("coordinate"))
    return coordinates


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--maven-install", default="maven_install.json")
    parser.add_argument("--metadata")
    parser.add_argument("--shared-metadata", action="append", default=[])
    parser.add_argument("--notice", default="NOTICE")
    parser.add_argument("--root-manifest", action="append", default=[])
    parser.add_argument("--runtime-jar", action="append", default=[])
    parser.add_argument("--first-party-group", action="append", default=[])
    parser.add_argument("--license-aliases")
    parser.add_argument("--output")
    parser.add_argument("--inventory-output")
    parser.add_argument("--delta-inventory")
    parser.add_argument("--baseline-inventory", action="append", default=[])
    parser.add_argument("--delta-json-output")
    parser.add_argument("--delta-markdown-output")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--update-metadata", action="store_true")
    parser.add_argument("--prune-shared-metadata", action="store_true")
    args = parser.parse_args()

    if args.delta_inventory:
        if not args.delta_json_output or not args.delta_markdown_output:
            parser.error(
                "--delta-inventory requires --delta-json-output and "
                "--delta-markdown-output"
            )
        inventory = load_json(pathlib.Path(args.delta_inventory))
        baselines = [
            load_json(pathlib.Path(path))
            for path in args.baseline_inventory
        ]
        delta = generated_delta(inventory, baselines)
        pathlib.Path(args.delta_json_output).write_text(
            json.dumps(delta, indent=2, sort_keys=True) + "\n"
        )
        pathlib.Path(args.delta_markdown_output).write_text(
            generated_delta_markdown(delta)
        )
        print(
            f"OSRB delta generated for {len(delta['dependencies'])} "
            "new or additional dependencies"
        )
        return 0

    if not args.metadata:
        parser.error("--metadata is required for NOTICE generation")

    maven_install_path = pathlib.Path(args.maven_install)
    metadata_path = pathlib.Path(args.metadata)
    notice_path = pathlib.Path(args.notice)
    maven_install = load_json(maven_install_path)
    primary_metadata = (
        load_json(metadata_path)
        if metadata_path.exists()
        else {"artifacts": {}}
    )
    shared_documents = [
        (path, load_json(pathlib.Path(path)))
        for path in args.shared_metadata
    ]

    if args.prune_shared_metadata:
        primary_metadata = prune_shared_metadata(
            primary_metadata,
            shared_documents,
        )
        metadata_path.write_text(
            json.dumps(primary_metadata, indent=2, sort_keys=True) + "\n"
        )

    shared_metadata = merge_metadata(
        shared_documents,
        {"artifacts": {}},
        False,
    )
    metadata = merge_metadata(shared_documents, primary_metadata)

    first_party_groups = tuple(FIRST_PARTY_GROUPS) + tuple(args.first_party_group)
    roots = load_root_manifests([pathlib.Path(path) for path in args.root_manifest])
    if args.runtime_jar:
        coordinates = runtime_jar_coordinates(
            [pathlib.Path(path) for path in args.runtime_jar],
            maven_install,
            first_party_groups,
        )
    elif roots:
        coordinates = dependency_closure(roots, maven_install, first_party_groups)
    else:
        raise ValueError("At least one --root-manifest or --runtime-jar is required")

    if args.update_metadata:
        primary_metadata = update_metadata(
            coordinates,
            maven_install,
            primary_metadata,
            shared_metadata,
        )
        metadata_path.write_text(
            json.dumps(primary_metadata, indent=2, sort_keys=True) + "\n"
        )
        metadata = merge_metadata(shared_documents, primary_metadata)

    notice = generated_notice(coordinates, maven_install, metadata)
    output_path = pathlib.Path(args.output) if args.output else notice_path
    diff = compare_or_write(notice, output_path, args.write and not args.check)
    if diff:
        sys.stderr.write(diff)
        sys.stderr.write(
            "\nRun the component's Bazel generate_notice target with --write "
            "(and --update-metadata when dependency metadata changed)\n"
        )
        return 1

    if args.inventory_output:
        inventory = generated_inventory(
            coordinates,
            maven_install,
            metadata,
            load_license_aliases(
                pathlib.Path(args.license_aliases)
                if args.license_aliases
                else None
            ),
        )
        pathlib.Path(args.inventory_output).write_text(
            json.dumps(inventory, indent=2, sort_keys=True) + "\n"
        )

    if args.check:
        generated_coordinates = set()
        for key in coordinates:
            artifact = maven_install["artifacts"].get(key)
            if artifact:
                group_id, artifact_id = key.split(":", 1)
                generated_coordinates.add(versioned_coordinate(group_id, artifact_id, artifact["version"]))
        notice_coordinates = current_notice_coordinates(notice_path)
        if notice_coordinates and notice_coordinates != generated_coordinates:
            missing = sorted(generated_coordinates - notice_coordinates)
            extra = sorted(notice_coordinates - generated_coordinates)
            if missing or extra:
                sys.stderr.write("NOTICE coordinate set does not match Bazel generated dependency closure\n")
                if missing:
                    sys.stderr.write("Missing from NOTICE:\n" + "\n".join(f"  {item}" for item in missing) + "\n")
                if extra:
                    sys.stderr.write("Extra in NOTICE:\n" + "\n".join(f"  {item}" for item in extra) + "\n")
                return 1

    print(f"NOTICE generated for {len(coordinates)} third-party dependencies")
    return 0


if __name__ == "__main__":
    sys.exit(main())
