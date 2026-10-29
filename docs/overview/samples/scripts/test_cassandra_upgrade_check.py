# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import base64
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("checker", Path(__file__).with_name("cassandra-upgrade-check.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class Checks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def save(self, name, value):
        path = self.root / name
        path.write_text(json.dumps(value), encoding="utf-8")
        return path

    def topology(self):
        sts = {"metadata": {"name": "cassandra", "uid": "sts", "generation": 1},
               "spec": {"replicas": 3}, "status": {"observedGeneration": 1, "replicas": 3, "readyReplicas": 3}}
        pods, pvcs = [], []
        for i in range(3):
            name = f"cassandra-{i}"
            pods.append({"metadata": {"name": name, "uid": f"pod-{i}", "ownerReferences": [
                {"controller": True, "kind": "StatefulSet", "name": "cassandra", "uid": "sts"}]},
                "spec": {"containers": [{"name": "cassandra", "image": "registry/source"}],
                         "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": f"data-{name}"}}]},
                "status": {"phase": "Running", "podIP": f"10.0.0.{i}", "conditions": [{"type": "Ready", "status": "True"}],
                           "containerStatuses": [{"name": "cassandra", "ready": True, "state": {"running": {}}, "imageID": "sha256:source"}]}})
            pvcs.append({"metadata": {"name": f"data-{name}", "uid": f"pvc-{i}"},
                         "spec": {"volumeName": f"pv-{i}"}, "status": {"phase": "Bound"}})
        ring = "\n".join(f"UN 10.0.0.{i} 1 KiB 8 ? 00000000-0000-0000-0000-00000000000{i} rack" for i in range(3))
        for i in range(3):
            (self.root / f"cassandra-{i}-ring.txt").write_text(ring, encoding="utf-8")
        return sts, {"items": pods}, {"items": pvcs}

    def write_topology(self, objects):
        for name, value in zip(("statefulset.json", "pods.json", "pvcs.json"), objects):
            self.save(name, value)

    def test_documented_capture_preserves_callers_member_variable(self):
        guide = Path(__file__).parents[2] / "release-notes/0.6.0-to-0.6.1-upgrade.md"
        text = guide.read_text(encoding="utf-8")
        function = text.split("capture_members() {", 1)[1].split("\n}", 1)[0]
        script = "set -eu\nassert_context() { :; }\nkcp() { :; }\n"
        script += "capture_members() {" + function + "\n}\n"
        script += 'pod=cassandra-0\ncapture_members "$1"\ntest "$pod" = cassandra-0\n'
        result = subprocess.run(["bash", "-c", script, "capture-test", str(self.root / "capture")],
                                capture_output=True, text=True)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertTrue((self.root / "capture/cassandra-2-ring.txt").exists())

    def test_topology_rejects_partial_unhealthy_and_unowned_state(self):
        original = self.topology()
        self.write_topology(original)
        baseline = checker.topology(self.root)
        self.assertEqual(3, len(baseline["members"]))
        changes = [
            lambda s, p, v: p["items"].pop(),
            lambda s, p, v: p["items"][0]["metadata"]["ownerReferences"][0].update(uid="other"),
            lambda s, p, v: p["items"][0]["status"]["containerStatuses"][0].update(ready=False),
            lambda s, p, v: p["items"][0]["metadata"].update(deletionTimestamp="now"),
            lambda s, p, v: v["items"].pop(),
            lambda s, p, v: v["items"][0]["status"].update(phase="Pending"),
            lambda s, p, v: v["items"][0]["metadata"].update(ownerReferences=[{"uid": "owner"}]),
            lambda s, p, v: s["spec"].update(replicas=1),
            lambda s, p, v: s["spec"].update(persistentVolumeClaimRetentionPolicy={"whenDeleted": "Delete"}),
            lambda s, p, v: s["status"].update(observedGeneration=0),
        ]
        for change in changes:
            with self.subTest(change=change):
                objects = copy.deepcopy(original)
                change(*objects)
                self.write_topology(objects)
                with self.assertRaises(ValueError):
                    checker.topology(self.root)
        self.write_topology(original)
        path = self.root / "cassandra-0-ring.txt"
        path.write_text(path.read_text(encoding="utf-8").replace("UN ", "DN ", 1), encoding="utf-8")
        with self.assertRaises(ValueError):
            checker.topology(self.root)

    def test_adoption_requires_new_pods_and_retained_storage(self):
        objects = self.topology()
        self.write_topology(objects)
        baseline = self.save("baseline.json", checker.topology(self.root))
        with self.assertRaises(ValueError):
            checker.topology(self.root, baseline)
        sts, pods, pvcs = objects
        sts["metadata"]["uid"] = "new-sts"
        sts["status"].update(updateRevision="new-revision", updatedReplicas=3)
        for pod in pods["items"]:
            pod["metadata"]["uid"] += "-new"
            pod["metadata"]["ownerReferences"][0]["uid"] = "new-sts"
            pod["metadata"]["labels"] = {"controller-revision-hash": "new-revision"}
            pod["spec"]["containers"][0]["image"] = "registry/target"
        self.write_topology(objects)
        checker.topology(self.root, baseline, image="registry/target")
        with self.assertRaises(ValueError):
            checker.topology(self.root, baseline, unchanged=True)
        pvcs["items"][0]["metadata"]["uid"] = "replacement-pvc"
        self.write_topology(objects)
        with self.assertRaises(ValueError):
            checker.topology(self.root, baseline, image="registry/target")

    def test_cql_result_requires_complete_trailer(self):
        self.assertEqual([{"dirty": False}], checker.cql_rows(' [json]\n {"dirty":false}\n(1 rows)\n'))
        for invalid in ('Connection error', '{"dirty":false}', '{"dirty":false}\n(2 rows)', '{bad}\n(1 rows)'):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                checker.cql_rows(invalid)

    def test_utf8_cql_rows_survive_an_ascii_locale(self):
        path = self.root / "accounts.txt"
        path.write_text(' {"name":"caf\u00e9"}\n(1 rows)\n', encoding="utf-8")
        with patch("io.text_encoding", side_effect=lambda encoding, *args: encoding or "ascii"):
            output = io.StringIO()
            with patch("sys.argv", [str(checker.__file__), "cql-json", str(path)]), patch("sys.stdout", output):
                checker.main()
            self.assertEqual(json.loads(output.getvalue()), [{"name": "caf\u00e9"}])

    def test_ledger_rejects_empty_dirty_missing_and_regressed_rows(self):
        self.save("migration-tables.json", [{"table_name": "nvcf_api"}])
        baseline = self.save("ledger-before.json", {"nvcf_api": 3})
        self.save("migration-nvcf_api.json", [{"version": 4, "dirty": False}])
        self.assertEqual({"nvcf_api": 4}, checker.ledger(self.root, baseline))
        for invalid in ([], [{"version": 4, "dirty": True}], [{"version": 2, "dirty": False}], [{"version": 4}]):
            self.save("migration-nvcf_api.json", invalid)
            with self.assertRaises(ValueError):
                checker.ledger(self.root, baseline)
        self.save("migration-tables.json", [])
        with self.assertRaises(ValueError):
            checker.ledger(self.root, baseline)

    def test_all_enabled_releases_must_match(self):
        expected = [{"enabled": True, "installed": True, "name": "api", "namespace": "nvcf", "chart": "nvcf/helm-nvcf-api", "version": "1.23.9"}]
        current = [{"name": "api", "namespace": "nvcf", "chart": "helm-nvcf-api-1.23.9", "status": "deployed"}]
        self.assertEqual(1, checker.releases(expected, current)["matched_releases"])
        for invalid in ([], [{**current[0], "chart": "helm-nvcf-api-1.23.6"}], [{**current[0], "status": "failed"}]):
            with self.assertRaises(ValueError):
                checker.releases(expected, invalid)
        with self.assertRaises(ValueError):
            checker.releases([{**expected[0], "enabled": False}], current)

    def test_jobs_must_be_new_and_complete(self):
        current = {"items": [{"metadata": {"name": n, "uid": n}, "status": {"conditions": [{"type": "Complete", "status": "True"}]}}
                             for n in ("cassandra-initialize-cluster", "cassandra-migrations")]}
        checker.jobs(current, {"items": []})
        with self.assertRaises(ValueError):
            checker.jobs(current, current)
        current["items"][1]["status"] = {}
        with self.assertRaises(ValueError):
            checker.jobs(current, {"items": []})

    def make_archive(self, missing=False):
        path = self.root / "snapshot.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            for keyspace in ("nvcf_api", "system_auth", "schema_migrations"):
                prefix = f"data/{keyspace}/table-id/snapshots/tag/"
                files = {"manifest.json": json.dumps({"files": ["nb-id-big-Data.db"]}).encode(),
                         "nb-id-big-Data.db": b"data", "nb-id-big-TOC.txt": b"Data.db\nStatistics.db\nIndex.db\nCompressionInfo.db\nTOC.txt\n",
                         "nb-id-big-Statistics.db": b"metadata", "nb-id-big-Index.db": b"index",
                         "nb-id-big-CompressionInfo.db": b"compression"}
                if missing:
                    files.pop("nb-id-big-Data.db")
                for name, data in files.items():
                    info = tarfile.TarInfo(prefix + name)
                    info.size = len(data)
                    archive.addfile(info, io.BytesIO(data))
        return path

    def test_archive_checks_manifest_tag_and_transfer_integrity(self):
        path = self.make_archive()
        self.assertEqual(checker.digest(path), checker.archive(path, "tag")["sha256"])
        with self.assertRaises(ValueError):
            checker.archive(path, "wrong-tag")
        with self.assertRaises(ValueError):
            checker.archive(self.make_archive(missing=True), "tag")
        path = self.make_archive()
        path.write_bytes(path.read_bytes()[:-16])
        with self.assertRaises((EOFError, OSError, tarfile.TarError)):
            checker.archive(path, "tag")

    def test_snapshot_without_toc_still_requires_primary_components(self):
        original = self.make_archive()
        for missing_index in (False, True):
            path = self.root / ("missing-index.tar.gz" if missing_index else "no-toc.tar.gz")
            with tarfile.open(original) as source, tarfile.open(path, "w:gz") as target:
                for entry in source:
                    if entry.name.endswith("TOC.txt") or (missing_index and entry.name.endswith("Index.db")):
                        continue
                    target.addfile(entry, source.extractfile(entry))
            if missing_index:
                with self.assertRaises(ValueError):
                    checker.archive(path, "tag")
            else:
                checker.archive(path, "tag")

    def test_no_toc_archive_rejects_unverifiable_sai_components(self):
        original = self.make_archive()
        path = self.root / "no-toc-sai.tar.gz"
        with tarfile.open(original) as source, tarfile.open(path, "w:gz") as target:
            for entry in source:
                if entry.name.endswith("TOC.txt"):
                    continue
                target.addfile(entry, source.extractfile(entry))
                if entry.name.endswith("Data.db"):
                    prefix = entry.name[:-len("Data.db")] + "SAI+aa+index+"
                    for component in ("ColumnComplete.db", "Meta.db"):
                        info = tarfile.TarInfo(prefix + component)
                        info.size = 1
                        target.addfile(info, io.BytesIO(b"x"))
        with self.assertRaises(ValueError):
            checker.archive(path, "tag")

    def test_archive_accepts_empty_sai_column_but_rejects_partial_nonempty_index(self):
        original = self.make_archive()
        for nonempty in (False, True):
            path = self.root / ("nonempty.tar.gz" if nonempty else "empty.tar.gz")
            with tarfile.open(original) as source, tarfile.open(path, "w:gz") as target:
                for entry in source:
                    data = source.extractfile(entry).read()
                    if entry.name.endswith("TOC.txt"):
                        data += b"SAI+aa+index+Meta.db\nSAI+aa+index+PostingLists.db\nSAI+aa+index+ColumnComplete.db\n"
                    entry.size = len(data)
                    target.addfile(entry, io.BytesIO(data))
                    if entry.name.endswith("TOC.txt"):
                        prefix = entry.name[:-len("TOC.txt")] + "SAI+aa+index+"
                        marker = tarfile.TarInfo(prefix + "ColumnComplete.db")
                        data = bytes.fromhex("3fd76c1702616101c02893e800000000000000005b1f9c3c")
                        marker.size = len(data)
                        target.addfile(marker, io.BytesIO(data))
                        if nonempty:
                            info = tarfile.TarInfo(prefix + "Meta.db")
                            info.size = 1
                            target.addfile(info, io.BytesIO(b"x"))
            if nonempty:
                with self.assertRaises(ValueError):
                    checker.archive(path, "tag")
            else:
                checker.archive(path, "tag")

    def test_space_stops_before_snapshot_when_capacity_is_insufficient(self):
        for i in range(3):
            (self.root / f"cassandra-{i}-space.txt").write_text("100 data\nFilesystem 1024-blocks Used Available Capacity Mounted\ndisk 2000 1000 1000 50% /data\n", encoding="utf-8")
        with patch.object(checker.shutil, "disk_usage", return_value=type("Usage", (), {"free": 10**9})()):
            checker.space(self.root)
        with patch.object(checker.shutil, "disk_usage", return_value=type("Usage", (), {"free": 1})()):
            with self.assertRaises(ValueError):
                checker.space(self.root)
        (self.root / "cassandra-0-space.txt").write_text("100 data\ndisk 2000 1999 1 99% /data\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            checker.space(self.root)

    def target_fixture(self):
        user, password, service = "nondefault_user", "nondefault_password", "service_password"
        values = self.save("values.json", {"cassandra": {"dbUser": {"user": user, "password": password}, "serviceRolePassword": service},
                                          "openbao": {"migrations": {"env": [{"name": "DEFAULT_CASSANDRA_PASSWORD", "value": service}]}}})
        secret = self.save("secret.json", {"data": {"cassandra-password": base64.b64encode(password.encode()).decode()}})
        env = [{"name": "CASSANDRA_CLUSTER_NAME", "value": "cassandra"}, {"name": "CASSANDRA_DATACENTER", "value": "ncp"}]
        source = self.save("source.json", {"spec": {"template": {"spec": {"containers": [
            {"name": "cassandra", "env": env + [{"name": "CASSANDRA_USER", "value": user}]}]}}}})
        env = [{"name": "CASSANDRA_CLUSTER_NAME", "value": "cassandra"}, {"name": "CASSANDRA_DC", "value": "ncp"}]
        objects = [{"kind": "StatefulSet", "metadata": {"name": "cassandra"}, "spec": {"replicas": 3, "template": {"spec": {"containers": [
            {"name": "cassandra", "env": env, "image": "registry/cassandra:5.0.8-nv-2.0.1",
             "volumeMounts": [{"name": "data", "subPath": "data", "mountPath": "/var/lib/cassandra"}]}]}}}}]
        objects[0]["spec"]["template"]["spec"]["securityContext"] = {"fsGroup": 999}
        objects[0]["spec"]["volumeClaimTemplates"] = [{"metadata": {"name": "data"}}]
        for name in ("cassandra-initialize-cluster", "cassandra-migrations", "openbao-server-migrations"):
            objects.append({"kind": "Job", "metadata": {"name": name}, "spec": {"template": {"spec": {"containers": [
                {"image": "registry/nvcf-cassandra-migrations:0.10.3", "env": [
                    {"name": "CASSANDRA_USER", "value": user}, {"name": "CASSANDRA_PASSWORD", "value": password},
                    {"name": "SERVICE_ROLE_PASSWORD", "value": service}, {"name": "DEFAULT_CASSANDRA_PASSWORD", "value": service}]}]}}}})
        configuration = "uuid_sstable_identifiers_enabled: true\nstorage_compatibility_mode: NONE\nauthenticator: PasswordAuthenticator\nauthorizer: CassandraAuthorizer\n"
        objects.append({"kind": "ConfigMap", "metadata": {"name": "cassandra-conf"}, "data": {"cassandra-overrides.yaml": configuration}})
        for i in range(3):
            (self.root / f"cassandra-{i}-cassandra.yaml").write_text(configuration, encoding="utf-8")
        return objects, values, secret, source

    def test_target_accepts_shipped_authentication_maps_and_rejects_custom_parameters(self):
        objects, values, secret, source = self.target_fixture()
        path = self.save("render.json", {"kind": "List", "items": objects})
        config = self.root / "cassandra-0-cassandra.yaml"
        shipped = config.read_text(encoding="utf-8").replace("authenticator: PasswordAuthenticator", "authenticator:\n  class_name: PasswordAuthenticator").replace("authorizer: CassandraAuthorizer", "authorizer:\n  class_name: CassandraAuthorizer")
        config.write_text(shipped, encoding="utf-8")
        checker.target(path, values, secret, source, self.root)
        for invalid in (shipped.replace("PasswordAuthenticator", "AllowAllAuthenticator"),
                        shipped.replace("  class_name: PasswordAuthenticator", "  class_name: PasswordAuthenticator\n  parameters: {}"),
                        shipped + "authenticator: PasswordAuthenticator\n"):
            config.write_text(invalid, encoding="utf-8")
            with self.assertRaises(ValueError):
                checker.target(path, values, secret, source, self.root)

    def test_target_render_preserves_real_chart_value_routes(self):
        objects, values, secret, source = self.target_fixture()
        path = self.save("render.json", {"kind": "List", "items": objects})
        self.assertEqual("registry/cassandra:5.0.8-nv-2.0.1", checker.target(path, values, secret, source, self.root)["image"])
        mutations = [
            lambda x: x[0]["spec"].update(replicas=1),
            lambda x: x[0]["spec"]["template"]["spec"]["containers"][0]["volumeMounts"][0].pop("subPath"),
            lambda x: x[0]["spec"]["template"]["spec"]["containers"][0]["env"][1].update(value="different-dc"),
            lambda x: x[1]["spec"]["template"]["spec"]["containers"][0]["env"][1].update(value="cassandra"),
            lambda x: x[2]["spec"]["template"]["spec"]["containers"][0]["env"][2].update(value="changed-service-password"),
            lambda x: x[3]["spec"]["template"]["spec"]["containers"][0]["env"][3].update(value="changed-bao-password"),
            lambda x: x[2]["spec"]["template"]["spec"]["containers"][0].update(image="registry/nvcf-cassandra-migrations:0.11.0"),
            lambda x: x[4]["data"].update({"cassandra-overrides.yaml": "storage_compatibility_mode: CASSANDRA_4\n"}),
        ]
        for mutate in mutations:
            altered = copy.deepcopy(objects)
            mutate(altered)
            self.save("render.json", {"kind": "List", "items": altered})
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                checker.target(path, values, secret, source, self.root)

    def test_bundle_rejects_wrong_archive_and_modified_template(self):
        stack = self.root / "stack-0.6.1"
        (stack / "helmfile.d").mkdir(parents=True)
        (stack / "environments").mkdir()
        archive_dir = self.root / "nvcf-self-managed-stack_v0.6.1"
        archive_dir.mkdir()
        path = archive_dir / "nvcf-self-managed-stack-0.6.1.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            for name in ("Makefile", "global.yaml.gotmpl", "environments/base.yaml", "helmfile.d/01-dependencies.yaml.gotmpl"):
                (stack / name).write_text("fixture", encoding="utf-8")
                archive.add(stack / name, arcname="./" + name)
        with self.assertRaises(ValueError):
            checker.bundle(stack, "0.6.1")
        expected = "644cad630b40ad564fb1406322372279fc7a8e5e0c534f94e0bbf9cf580576ca"
        with patch.object(checker, "digest", return_value=expected):
            checker.bundle(stack, "0.6.1")
            (stack / "global.yaml.gotmpl").write_text("wrong release", encoding="utf-8")
            with self.assertRaises(ValueError):
                checker.bundle(stack, "0.6.1")

    def test_wrong_context_stops_the_documented_shell_gate(self):
        guide = Path(__file__).parents[2] / "release-notes/0.6.0-to-0.6.1-upgrade.md"
        text = guide.read_text(encoding="utf-8")
        function = text.split("assert_context() {", 1)[1].split("\n}", 1)[0]
        script = 'set -euo pipefail\nCONTROL_KUBECONFIG=fake\nCONTROL_CONTEXT=expected\nCONTROL_CLUSTER_UID=cluster\n'
        script += 'kubectl() { echo wrong-context; }\nkcp() { echo cluster; }\n'
        script += 'assert_context() {' + function + '\n}\nassert_context\necho MUTATION_REACHED\n'
        result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("MUTATION_REACHED", result.stdout)

    def test_documented_source_release_gate_uses_helm_list_shape(self):
        guide = Path(__file__).parents[2] / "release-notes/0.6.0-to-0.6.1-upgrade.md"
        query = re.search(r"jq -e '([^']+)' \\\n  \"\$BACKUP_DIR/cassandra-release.json\"", guide.read_text(encoding="utf-8")).group(1)
        valid = {"name": "cassandra", "namespace": "cassandra-system", "status": "deployed", "chart": "helm-nvcf-cassandra-0.15.5"}
        for records, expected in (([valid], 0), ([], 1), ([valid, valid], 1),
                                  ([{**valid, "status": "failed"}], 1),
                                  ([{**valid, "chart": "helm-nvcf-cassandra-0.18.0"}], 1),
                                  ([{**valid, "namespace": "unrelated"}], 1)):
            result = subprocess.run(["jq", "-e", query], input=json.dumps(records), text=True, capture_output=True)
            self.assertEqual(expected, result.returncode)


class SessionChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.upgrade = self.root / "upgrade 'quoted' $(touch INJECTED)"
        self.upgrade.mkdir()
        self.backup = self.upgrade / "backup"
        self.backup.mkdir()
        self.guide = (Path(__file__).parents[2] / "release-notes/0.6.0-to-0.6.1-upgrade.md").read_text(encoding="utf-8")
        self.env = {
            "PATH": os.environ["PATH"], "UPGRADE_DIR": str(self.upgrade),
            "BACKUP_DIR": str(self.backup), "HELMFILE_ENV": "site 'quoted' $(touch INJECTED)",
            "CONTROL_KUBECONFIG": str(self.root / "kube config"),
            "CONTROL_CONTEXT": "expected", "CONTROL_CLUSTER_UID": "original-cluster",
            "SOURCE_CONTROL": str(self.upgrade / "source"), "TARGET_CONTROL": str(self.upgrade / "target"),
            "CQL_USER": "fixture-user", "CQL_PASSWORD": "fixture-password", "UNRELATED_TOKEN": "fixture-token",
            "LIVE_CONTEXT": "expected", "LIVE_UID": "original-cluster",
        }
        self.stub = '''
kubectl() {
  if [ "$3" = config ]; then printf '%s\\n' "$LIVE_CONTEXT"
  elif [ "$5 $6 $7" = 'get namespace kube-system' ]; then printf '%s\\n' "$LIVE_UID"
  elif [ "$5 $6 $7 $8" = '-n cassandra-system get pods' ]; then echo DIAGNOSTICS_REACHED
  else return 99
  fi
}
'''

    def function(self, name):
        return re.search(rf"^{name}\(\) \{{\n.*?^\}}", self.guide, re.M | re.S).group(0) + "\n"

    def run_shell(self, script, **changes):
        return subprocess.run(["bash", "--noprofile", "--norc", "-c", script],
                              cwd=self.root, env={**self.env, **changes}, text=True, capture_output=True)

    def checkpoint(self, late=False):
        script = "set -euo pipefail\numask 077\n"
        script += "".join(self.function(name) for name in ("kcp", "assert_context", "save_session"))
        script += "save_session\n"
        if late:
            script += "".join(self.function(name) for name in
                              ("capture_members", "load_cql_credentials", "cql", "cql_json", "capture_data"))
            script += '''
CHECKER="$UPGRADE_DIR/cassandra-upgrade-check.py"
CQL_POD=cassandra-0
SNAPSHOT_TAG=original-snapshot
TARGET_CASSANDRA_IMAGE=registry.example/cassandra:target
save_session
'''
        script += "false\necho MUTATION_REACHED\n"
        result = self.run_shell(script)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("MUTATION_REACHED", result.stdout)
        self.assertTrue((self.upgrade / "session.sh").is_file(), result.stderr)

    def test_failure_recovery_preserves_values_and_excludes_credentials(self):
        self.checkpoint()
        session = self.upgrade / "session.sh"
        self.assertEqual(session.stat().st_mode & 0o777, 0o600)
        contents = session.read_text(encoding="utf-8")
        for value in ("fixture-user", "fixture-password", "fixture-token"):
            self.assertNotIn(value, contents)
        script = self.stub + '''
source "$UPGRADE_DIR/session.sh"
assert_context
kcp -n cassandra-system get pods
test "${CQL_USER+x}${CQL_PASSWORD+x}" = ''
printf '%s\\n' "$HELMFILE_ENV" "$CONTROL_CLUSTER_UID" "$BACKUP_DIR"
false
echo MUTATION_REACHED
'''
        result = self.run_shell(script, CONTROL_CONTEXT="inherited-wrong", CONTROL_CLUSTER_UID="inherited-wrong")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout.splitlines(), ["DIAGNOSTICS_REACHED", self.env["HELMFILE_ENV"],
                                                       "original-cluster", str(self.backup)])
        self.assertFalse((self.root / "INJECTED").exists())

    def test_recovery_rejects_changed_context_and_cluster(self):
        self.checkpoint()
        for changed in ({"LIVE_CONTEXT": "other"}, {"LIVE_UID": "other"}):
            with self.subTest(changed=changed):
                result = self.run_shell(self.stub + 'source "$UPGRADE_DIR/session.sh"\nassert_context\necho MUTATION_REACHED\n',
                                        **changed)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("MUTATION_REACHED", result.stdout)

    def test_later_checkpoint_restores_helpers_and_original_credentials(self):
        self.checkpoint(late=True)
        (self.backup / "cassandra-values.json").write_text(
            json.dumps({"cassandra": {"dbUser": {"user": "recorded-user"}}}), encoding="utf-8")
        (self.backup / "cassandra-secret.json").write_text(
            json.dumps({"data": {"cassandra-password": base64.b64encode(b"recorded-password").decode()}}), encoding="utf-8")
        script = self.stub + '''
source "$UPGRADE_DIR/session.sh"
assert_context
for helper in capture_members load_cql_credentials cql cql_json capture_data; do
  declare -F "$helper" >/dev/null
done
test "$SNAPSHOT_TAG" = original-snapshot
test "$TARGET_CASSANDRA_IMAGE" = registry.example/cassandra:target
test "$CHECKER" = "$UPGRADE_DIR/cassandra-upgrade-check.py"
test "$CQL_POD" = cassandra-0
load_cql_credentials
test "$CQL_USER" = recorded-user
test "$CQL_PASSWORD" = recorded-password
save_session
'''
        result = self.run_shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("recorded-password", (self.upgrade / "session.sh").read_text(encoding="utf-8"))

    def test_rerun_setup_preserves_existing_evidence(self):
        self.checkpoint(late=True)
        baseline = self.backup / "baseline.json"
        baseline.write_text('{"original":true}', encoding="utf-8")
        session = (self.upgrade / "session.sh").read_bytes()
        guard = re.search(r'^if \[ -e "\$UPGRADE_DIR" \]; then\n.*?^fi', self.guide, re.M | re.S).group(0)
        result = self.run_shell(guard + "\necho SETUP_REACHED\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("SETUP_REACHED", result.stdout)
        self.assertIn("saved session", result.stderr)
        self.assertEqual((self.upgrade / "session.sh").read_bytes(), session)
        self.assertEqual(baseline.read_text(encoding="utf-8"), '{"original":true}')


if __name__ == "__main__":
    unittest.main()
