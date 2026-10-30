# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import hashlib
import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from java_runtime_contract import BROTLI_FLOOR, merged_files, resolve, version_compare


class JavaRuntimeContractTest(unittest.TestCase):
    def test_brotli_security_floor(self):
        for version in ("1.1.0-2", "1.1.0-2+deb13u0", "1.1.0-2+deb13u1~rc1"):
            with self.subTest(version=version):
                self.assertLess(version_compare(version, BROTLI_FLOOR), 0)
        for version in (BROTLI_FLOOR, "1.1.0-2+deb13u10", "1.1.0-3", "1.2.0-1", "1:1.0.0-1"):
            with self.subTest(version=version):
                self.assertGreaterEqual(version_compare(version, BROTLI_FLOOR), 0)

    def layer(self, root, entries):
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w") as archive:
            for name, content in entries.items():
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


if __name__ == "__main__":
    unittest.main()
