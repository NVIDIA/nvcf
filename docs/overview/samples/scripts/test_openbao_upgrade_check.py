# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
import re
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("checker", Path(__file__).with_name("openbao-upgrade-check.py"))
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)
OLD_IMAGE = "registry.example/nvcf-openbao:2.5.4-nv-1.3.0"
NEW_IMAGE = "registry.example/nvcf-openbao:2.5.5-nv-1.3.1"
OLD_ID, NEW_ID = "sha256:" + "a" * 64, "sha256:" + "b" * 64
OLD_SHA, NEW_SHA = "c" * 64, "d" * 64


def put(root, name, value):
    (root / name).write_text(json.dumps(value), encoding="utf-8")


def fixture(root):
    root.mkdir()
    containers = [{"name": n, "image": NEW_IMAGE,
                   "volumeMounts": [{"name": "data", "mountPath": "/openbao/data"}]}
                  for n in sorted(checker.CONTAINERS)]
    put(root, "statefulset.json", {
        "metadata": {"name": "openbao-server", "namespace": "vault-system", "uid": "sts", "generation": 2},
        "spec": {"replicas": 3, "updateStrategy": {"type": "OnDelete"},
                 "template": {"spec": {"containers": containers}}},
        "status": {"observedGeneration": 2, "currentRevision": "historical", "updateRevision": "target",
                   "updatedReplicas": 0}})
    pods, pvcs, peers, servers = [], [], [], {}
    for i, name in enumerate(checker.PODS):
        pod_containers = copy.deepcopy(containers)
        for c in pod_containers:
            c["image"] = OLD_IMAGE
        pods.append({"metadata": {"name": name, "namespace": "vault-system", "uid": "old-" + name,
                                   "labels": {"controller-revision-hash": "source"},
                                   "ownerReferences": [{"uid": "sts", "controller": True}]},
                     "spec": {"containers": pod_containers, "nodeName": "node",
                              "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "data-" + name}}]},
                     "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}],
                                "containerStatuses": [{"name": n, "ready": True, "state": {"running": {}},
                                                       "imageID": OLD_IMAGE + "@" + OLD_ID}
                                                      for n in sorted(checker.CONTAINERS)]}})
        pvcs.append({"metadata": {"name": "data-" + name, "uid": "pvc-" + name},
                     "spec": {"volumeName": "pv-" + name}, "status": {"phase": "Bound"}})
        status = {"initialized": True, "sealed": False, "ha_enabled": True,
                  "type": "shamir", "storage_type": "raft", "version": "2.5.4",
                  "leader_address": "http://10.0.0.10:8200",
                  "leader_cluster_address": "https://openbao-server-0.openbao-server-internal:8201"}
        if i == 0:
            status["is_self"] = True
        put(root, name + "-status.json", status)
        (root / (name + "-node-id.txt")).write_text("raft-" + name, encoding="utf-8")
        (root / (name + "-plugin.txt")).write_text(OLD_SHA + "  /openbao/plugins/vault-plugin-secrets-jwt\n", encoding="utf-8")
        peers.append({"node_id": "raft-" + name, "voter": True, "leader": i == 0})
        servers["raft-" + name] = {"healthy": True}
    put(root, "pods.json", {"items": pods})
    put(root, "pvcs.json", {"items": pvcs})
    put(root, "nodes.json", {"items": [{"metadata": {"name": "node", "labels": {"kubernetes.io/arch": "arm64"}}}]})
    put(root, "peers.json", {"data": {"config": {"servers": peers}}})
    put(root, "autopilot.json", {"data": {"healthy": True, "failure_tolerance": 1, "voters": sorted(servers),
                                           "servers": servers, "leader": "raft-openbao-server-0"}})
    put(root, "catalog.json", {"data": {"command": checker.PLUGIN, "sha256": OLD_SHA}})


class UpgradeChecks(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.base, self.current, self.target = [self.root / s for s in ("base", "current", "target")]
        fixture(self.base)
        fixture(self.current)
        self.target.mkdir()
        put(self.target, "statefulset.json", checker.read(self.current, "statefulset.json"))
        put(self.target, "rendered-statefulset.json", checker.read(self.current, "statefulset.json"))
        put(self.target, "image.json", {"image": NEW_IMAGE, "image_id": NEW_IMAGE + "@" + NEW_ID,
                                       "plugin_sha256": NEW_SHA})
        self.edit("catalog.json", lambda j: j["data"].update(sha256=NEW_SHA))

    def edit(self, name, change):
        data = checker.read(self.current, name)
        change(data)
        put(self.current, name, data)

    def run_check(self, stage="transition", **kwargs):
        return checker.check(self.current, stage, self.base, self.target, **kwargs)

    def replace(self, index):
        name = checker.PODS[index]
        def update(j):
            pod = j["items"][index]
            pod["metadata"]["uid"] = "new-" + name
            pod["metadata"]["labels"]["controller-revision-hash"] = "target"
            for c in pod["spec"]["containers"]:
                c["image"] = NEW_IMAGE
            for c in pod["status"]["containerStatuses"]:
                c["imageID"] = NEW_IMAGE + "@" + NEW_ID
        self.edit("pods.json", update)
        self.edit(name + "-status.json", lambda j: j.update(version="2.5.5"))
        (self.current / (name + "-plugin.txt")).write_text(NEW_SHA + "  plugin\n", encoding="utf-8")

    def test_unchanged_template_still_selects_inherited_stale_pods(self):
        self.assertEqual(self.run_check()["stale"], checker.PODS)

    def test_utf8_evidence_survives_an_ascii_locale(self):
        data = checker.read(self.current, "statefulset.json")
        data["metadata"]["annotations"] = {"description": "caf\u00e9"}
        (self.current / "statefulset.json").write_text(json.dumps(data, ensure_ascii=False), encoding="utf-8")
        events = self.root / "events.json"
        events.write_text(json.dumps({"object": data}, ensure_ascii=False), encoding="utf-8")
        (self.current / (checker.PODS[0] + "-plugin.txt")).write_text(
            OLD_SHA + "  /plugins/caf\u00e9\n", encoding="utf-8")
        with patch("io.text_encoding", side_effect=lambda encoding, *args: encoding or "ascii"):
            self.assertEqual(checker.read(self.current, "statefulset.json"), data)
            self.assertEqual(list(checker.watch_events(events)), [data])
            self.assertEqual(self.run_check()["stale"], checker.PODS)

    def test_api_omitted_host_network_false_matches_reviewed_render(self):
        rendered = checker.read(self.target, "rendered-statefulset.json")
        rendered["spec"]["template"]["spec"]["hostNetwork"] = False
        put(self.target, "rendered-statefulset.json", rendered)
        self.assertEqual(self.run_check()["stale"], checker.PODS)

    def test_host_network_default_does_not_accept_true_or_other_fields(self):
        cases = [
            ({}, {"hostNetwork": True}),
            ({"hostNetwork": True}, {"hostNetwork": False}),
            ({"hostNetwork": None}, {"hostNetwork": False}),
            ({}, {"automountServiceAccountToken": False}),
        ]
        for actual, rendered in cases:
            with self.subTest(actual=actual, rendered=rendered):
                self.assertFalse(checker.matches_render(
                    {"template": {"spec": actual}}, {"template": {"spec": rendered}}))
        self.assertFalse(checker.matches_render({}, {"hostNetwork": False}))

    def test_catalog_mismatch_is_allowed_only_for_retained_source_binaries(self):
        self.assertEqual(len(self.run_check()["stale"]), 3)
        with self.assertRaisesRegex(ValueError, "source catalog"):
            checker.check(self.current)

    def test_partial_progress_keeps_current_standby_out_of_selection(self):
        self.replace(1)
        self.assertEqual(self.run_check()["stale"], [checker.PODS[0], checker.PODS[2]])

    def test_ondelete_historical_current_revision_is_not_a_failure(self):
        for i in range(3):
            self.replace(i)
        self.edit("statefulset.json", lambda j: j["status"].update(updatedReplicas=3))
        self.assertEqual(self.run_check("complete")["stale"], [])
        self.assertNotEqual(checker.read(self.current, "statefulset.json")["status"]["currentRevision"], "target")

    def test_already_current_source_is_supported(self):
        for i in range(3):
            self.replace(i)
        self.assertEqual(checker.check(self.current)["stale"], [])

    def test_omitted_standby_flag_requires_corroborated_leader(self):
        self.assertEqual(self.run_check()["active"], checker.PODS[0])
        self.edit(checker.PODS[1] + "-status.json", lambda j: j.update(is_self=False))
        self.assertEqual(self.run_check()["active"], checker.PODS[0])
        self.edit(checker.PODS[1] + "-status.json", lambda j: j.update(leader_address="http://10.0.0.20:8200"))
        with self.assertRaisesRegex(ValueError, "leader addresses"):
            self.run_check()

    def test_ready_but_sealed_stops(self):
        self.edit(checker.PODS[1] + "-status.json", lambda j: j.update(sealed=True))
        with self.assertRaisesRegex(ValueError, "not initialized"):
            self.run_check()

    def test_negative_guards(self):
        cases = [
            ("pods.json", lambda j: j["items"].pop(), "three server"),
            ("pods.json", lambda j: j["items"][1]["status"]["containerStatuses"].clear(), "incomplete"),
            ("pods.json", lambda j: j["items"][1]["metadata"]["ownerReferences"][0].update(uid="other"), "owner"),
            ("pods.json", lambda j: j["items"][1]["metadata"].update(uid="surprise"), "unchanged source"),
            ("peers.json", lambda j: j["data"]["config"]["servers"][1].update(voter=False), "membership"),
            ("pvcs.json", lambda j: j["items"][1]["metadata"].update(uid="replacement"), "pvc changed"),
            ("pvcs.json", lambda j: j["items"][1]["metadata"].update(deletionTimestamp="2026-01-01T00:00:00Z",
                finalizers=["kubernetes.io/pvc-protection"]), "PVC is deleting"),
            ("pvcs.json", lambda j: j["items"][1]["metadata"].update(ownerReferences=[
                {"kind": "Pod", "uid": "old-openbao-server-1"}]), "ownership"),
            ("autopilot.json", lambda j: j["data"]["servers"]["raft-openbao-server-1"].update(healthy=False), "Autopilot"),
            ("catalog.json", lambda j: j["data"].update(sha256=OLD_SHA), "target hash"),
            ("statefulset.json", lambda j: j["spec"].update(replicas=1), "three members"),
            ("statefulset.json", lambda j: j["spec"].update(
                persistentVolumeClaimRetentionPolicy={"whenScaled": "Delete"}), "retention"),
            ("statefulset.json", lambda j: j["spec"]["template"].update(metadata={"labels": {"changed": "yes"}}), "frozen"),
            (checker.PODS[0] + "-status.json", lambda j: j.pop("is_self"), "one active"),
            (checker.PODS[1] + "-status.json", lambda j: j.update(is_self=True), "one active"),
            (checker.PODS[1] + "-status.json", lambda j: j.update(is_self=None), "not initialized"),
            (checker.PODS[1] + "-status.json", lambda j: j.update(type="awskms"), "Shamir"),
        ]
        for filename, change, message in cases:
            with self.subTest(message=message):
                saved = (self.current / filename).read_text(encoding="utf-8")
                self.edit(filename, change)
                with self.assertRaisesRegex(ValueError, message):
                    self.run_check()
                (self.current / filename).write_text(saved, encoding="utf-8")

    def test_other_architecture_node_rejects_unpinned_refresh_hook(self):
        self.edit("nodes.json", lambda j: j["items"].append({"metadata": {
            "name": "unused-amd-node", "labels": {"kubernetes.io/arch": "amd64"}}}))
        with self.assertRaisesRegex(ValueError, "every cluster node"):
            self.run_check()

    def test_wrong_replacement_digest_or_revision_stops(self):
        self.replace(1)
        self.edit("pods.json", lambda j: j["items"][1]["metadata"]["labels"].update({"controller-revision-hash": "wrong"}))
        with self.assertRaisesRegex(ValueError, "unchanged source"):
            self.run_check()
        self.replace(1)
        self.edit("pods.json", lambda j: [s.update(imageID=OLD_IMAGE + "@" + OLD_ID)
                                         for s in j["items"][1]["status"]["containerStatuses"]])
        with self.assertRaisesRegex(ValueError, "unchanged source"):
            self.run_check()

    def test_leader_guard_stops_before_deletion(self):
        with self.assertRaisesRegex(ValueError, "active member changed"):
            self.run_check(leader="openbao-server-1", leader_uid="old-openbao-server-1")

    def test_fresh_hook_evidence_required(self):
        events = self.root / "events.json"
        def job(name, uid, created, complete):
            return {"type": "DELETED", "object": {"kind": "Job", "metadata": {"name": name, "uid": uid,
                    "creationTimestamp": created}, "status": {"completionTime": complete,
                    "conditions": [{"type": "Complete", "status": "True"}]}}}
        refresh = job("openbao-server-refresh-jwt-plugin-catalog", "refresh", "2026-01-01T00:00:01Z", "2026-01-01T00:00:05Z")
        migration = job("openbao-server-migrations", "new", "2026-01-01T00:00:06Z", "2026-01-01T00:00:09Z")
        events.write_text(json.dumps(refresh) + "\n" + json.dumps(migration), encoding="utf-8")
        self.assertEqual(checker.check_hooks(events, "old", "2026-01-01T00:00:00Z")["migration_uid"], "new")
        with self.assertRaisesRegex(ValueError, "fresh completed hook"):
            checker.check_hooks(events, "new", "2026-01-01T00:00:00Z")
        events.write_text(json.dumps(migration), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "fresh completed hook"):
            checker.check_hooks(events, "old", "2026-01-01T00:00:00Z")
        migration["object"]["metadata"]["creationTimestamp"] = "2026-01-01T00:00:02Z"
        events.write_text(json.dumps(refresh) + json.dumps(migration), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "before catalog"):
            checker.check_hooks(events, "old", "2026-01-01T00:00:00Z")

    def test_documented_helm_gate_accepts_list_shape_only(self):
        guide = Path(__file__).parents[2] / "release-notes/0.6.1-to-0.6.2-upgrade.md"
        query = re.search(r"jq -e '([^']+)' \\\n    \"\$SYNC_DIR/helm.json\"", guide.read_text(encoding="utf-8")).group(1)
        release = {"name": "openbao-server", "namespace": "vault-system", "status": "deployed",
                   "chart": "helm-nvcf-openbao-server-0.32.6"}
        cases = [([release], 0), ([], 1), ([release, release], 1),
                 ([{**release, "chart": "helm-nvcf-openbao-server-0.30.25"}], 1),
                 ([{**release, "namespace": "other"}], 1), ([{**release, "status": "failed"}], 1),
                 ({"info": {"status": "deployed"}}, 1)]
        for value, code in cases:
            with self.subTest(value=value):
                result = subprocess.run(["jq", "-e", query], input=json.dumps(value),
                                        text=True, capture_output=True)
                self.assertEqual(result.returncode == 0, code == 0)


if __name__ == "__main__":
    unittest.main()
