#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
workspace="${TEST_TMPDIR:-/tmp}/lcov-to-sonar-test"
mkdir -p "${workspace}"

input="${workspace}/coverage.dat"
output="${workspace}/sonar-coverage.xml"

cat >"${input}" <<'LCOV'
SF:module/src/main/java/Foo.java
DA:10,1
DA:11,0
BRDA:10,0,0,1
BRDA:10,0,1,-
end_of_record
SF:module/src/main/java/Foo.java
DA:10,2
DA:12,0
BRDA:12,0,0,3
end_of_record
SF:module/src/main/java/Bar.java
DA:7,0
end_of_record
LCOV

python3 "${script_dir}/lcov_to_sonar_generic.py" \
  --input "${input}" \
  --output "${output}"

grep -F '<file path="module/src/main/java/Bar.java">' "${output}"
grep -F '<lineToCover lineNumber="7" covered="false" />' "${output}"
grep -F '<file path="module/src/main/java/Foo.java">' "${output}"
grep -F '<lineToCover lineNumber="10" covered="true" branchesToCover="2" coveredBranches="1" />' "${output}"
grep -F '<lineToCover lineNumber="11" covered="false" />' "${output}"
grep -F '<lineToCover lineNumber="12" covered="true" branchesToCover="1" coveredBranches="1" />' "${output}"
