#!/usr/bin/env python3
"""Convert Bazel LCOV output to SonarQube generic coverage XML."""

import argparse
import sys
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from pathlib import PurePosixPath
from typing import Dict, Optional, Tuple


@dataclass
class LineCoverage:
    hits: int = 0
    branches: Dict[Tuple[str, str], bool] = field(default_factory=dict)


def normalize_path(path: str) -> str:
    normalized = str(PurePosixPath(path))
    if normalized.startswith("./"):
        return normalized[2:]
    return normalized


def parse_lcov(path: str) -> Dict[str, Dict[int, LineCoverage]]:
    files: Dict[str, Dict[int, LineCoverage]] = {}
    current_file: Optional[str] = None

    with open(path, encoding="utf-8") as lcov:
        for raw_line in lcov:
            line = raw_line.strip()
            if not line:
                continue

            if line.startswith("SF:"):
                current_file = normalize_path(line[3:])
                files.setdefault(current_file, {})
                continue

            if line == "end_of_record":
                current_file = None
                continue

            if current_file is None:
                continue

            if line.startswith("DA:"):
                payload = line[3:]
                fields = payload.split(",", 2)
                if len(fields) < 2:
                    raise ValueError(f"Invalid LCOV DA entry: {line}")
                line_number = int(fields[0])
                hits = int(fields[1])
                line_coverage = files[current_file].setdefault(line_number, LineCoverage())
                line_coverage.hits += hits
                continue

            if line.startswith("BRDA:"):
                payload = line[5:]
                fields = payload.split(",", 3)
                if len(fields) != 4:
                    raise ValueError(f"Invalid LCOV BRDA entry: {line}")
                line_number = int(fields[0])
                branch_key = (fields[1], fields[2])
                taken = 0 if fields[3] == "-" else int(fields[3])
                line_coverage = files[current_file].setdefault(line_number, LineCoverage())
                line_coverage.branches[branch_key] = (
                    line_coverage.branches.get(branch_key, False) or taken > 0
                )

    return files


def build_sonar_xml(files: Dict[str, Dict[int, LineCoverage]]) -> ET.ElementTree:
    root = ET.Element("coverage", {"version": "1"})

    for file_path in sorted(files):
        file_element = ET.SubElement(root, "file", {"path": file_path})
        for line_number in sorted(files[file_path]):
            coverage = files[file_path][line_number]
            covered = coverage.hits > 0 or any(coverage.branches.values())
            attributes = {
                "lineNumber": str(line_number),
                "covered": "true" if covered else "false",
            }
            if coverage.branches:
                attributes["branchesToCover"] = str(len(coverage.branches))
                attributes["coveredBranches"] = str(
                    sum(1 for branch_covered in coverage.branches.values() if branch_covered)
                )
            ET.SubElement(file_element, "lineToCover", attributes)

    if hasattr(ET, "indent"):
        ET.indent(root, space="  ")
    return ET.ElementTree(root)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Convert Bazel's LCOV coverage report to Sonar generic XML."
    )
    parser.add_argument("--input", required=True, help="Input LCOV file.")
    parser.add_argument(
        "--output",
        required=True,
        help="Output Sonar generic coverage XML file. Use '-' for stdout.",
    )
    args = parser.parse_args()

    tree = build_sonar_xml(parse_lcov(args.input))

    if args.output == "-":
        tree.write(sys.stdout, encoding="unicode", xml_declaration=True)
        sys.stdout.write("\n")
    else:
        tree.write(args.output, encoding="utf-8", xml_declaration=True)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
