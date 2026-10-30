# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import hashlib
import io
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

from java_runtime_contract import BROTLI_FLOOR, merged_files, resolve, version_compare


class JavaRuntimeContractTest(unittest.TestCase):
    def test_brotli_security_floor(self):
        for version in ("1.1.0-2", "1.1.0-2+b7", "1.1.0-2+deb13u0", "1.1.0-2+deb13u1~rc1"):
            with self.subTest(version=version):
                self.assertLess(version_compare(version, BROTLI_FLOOR), 0)
        for version in (BROTLI_FLOOR, "1.1.0-2+deb13u10", "1.1.0-3", "1.2.0-1", "1:1.0.0-1"):
            with self.subTest(version=version):
                self.assertGreaterEqual(version_compare(version, BROTLI_FLOOR), 0)

    def layer(self, root, entries):
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w") as archive:
            for name, content in entries.items():
                if isinstance(content, tarfile.TarInfo):
                    archive.addfile(content)
                    continue
                member = tarfile.TarInfo(name)
                member.size = len(content)
                archive.addfile(member, io.BytesIO(content))
        digest = hashlib.sha256(data.getvalue()).hexdigest()
        path = root / "blobs" / "sha256" / digest
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data.getvalue())
        return {"digest": "sha256:" + digest}

    def test_later_package_layer_wins(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            layers = [self.layer(root, {"var/lib/dpkg/status.d/libbrotli1": value})
                      for value in (b"fixed", b"vulnerable")]
            record = resolve(merged_files(root, layers), "var/lib/dpkg/status.d/libbrotli1")
            self.assertEqual(record[1], b"vulnerable")

    def test_whiteouts_remove_files_and_opaque_directories(self):
        for whiteout in ("runtime/.wh.java", "runtime/.wh..wh..opq", ".wh.runtime", ".wh..wh..opq"):
            with self.subTest(whiteout=whiteout), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                layers = [
                    self.layer(root, {"runtime/java": b"binary"}),
                    self.layer(root, {whiteout: b""}),
                ]
                self.assertNotIn("runtime/java", merged_files(root, layers))

    def test_whiteout_does_not_delete_same_layer_replacement(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            layers = [self.layer(root, {"runtime/java": b"old"}),
                      self.layer(root, {"runtime/java": b"new", "runtime/.wh..wh..opq": b""})]
            self.assertEqual(resolve(merged_files(root, layers), "runtime/java")[1], b"new")

    def test_absolute_and_relative_java_symlinks(self):
        java = tarfile.TarInfo("jdk/bin/java")
        java.mode = 0o755
        for link in ("/jdk/bin/java", "../../jdk/bin/java"):
            with self.subTest(link=link):
                shim = tarfile.TarInfo("usr/bin/java")
                shim.type, shim.linkname = tarfile.SYMTYPE, link
                files = {"jdk/bin/java": (java, b"binary"), "usr/bin/java": (shim, b"")}
                self.assertEqual(resolve(files, "/usr/bin/java"), (java, b"binary"))

    def test_replacing_directory_removes_descendants(self):
        link = tarfile.TarInfo("runtime")
        link.type, link.linkname = tarfile.SYMTYPE, "elsewhere"
        for replacement in (b"file", link):
            with self.subTest(replacement=replacement), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                layers = [self.layer(root, {"runtime/java": b"old"}),
                          self.layer(root, {"runtime": replacement})]
                self.assertNotIn("runtime/java", merged_files(root, layers))

    def test_java_home_directory_symlink(self):
        link = tarfile.TarInfo("jdk/current")
        link.type, link.linkname = tarfile.SYMTYPE, "25"
        release = tarfile.TarInfo("jdk/25/release")
        files = {"jdk/current": (link, b""), "jdk/25/release": (release, b"Java 25")}
        self.assertEqual(resolve(files, "/jdk/current/release"), (release, b"Java 25"))

    def test_non_directory_ancestor_is_rejected(self):
        files = {name: (tarfile.TarInfo(name), b"") for name in ("jdk", "jdk/release")}
        with self.assertRaisesRegex(ValueError, "non-directory"):
            resolve(files, "/jdk/release")

    def test_missing_platforms_fail_with_python_optimization(self):
        checker = Path(__file__).with_name("java_runtime_contract.py")
        result = subprocess.run([sys.executable, "-O", str(checker)],
                                capture_output=True, text=True, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("expected both runtime platforms", result.stderr)


if __name__ == "__main__":
    unittest.main()
