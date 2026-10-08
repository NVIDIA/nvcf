# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import hashlib
import json
import pathlib
import py_compile
import shutil
import subprocess
import tarfile
import tempfile
import unittest

import yaml

HERE = pathlib.Path(__file__).resolve().parents[1]


@unittest.skipUnless(shutil.which('helm'), 'Helm is required')
class ChartPackageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='helm-package-')
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.source = self.root/'checkout/deploy/helm/llm-routing'
        self.source.mkdir(parents=True)
        self.output = self.root/'published'
        for name in ('llm-gateway-stack', 'llm-api-gateway', 'llm-request-router', 'pylon-operator'):
            shutil.copytree(HERE.parent/name/name, self.source.parent/name/name,
                            ignore=shutil.ignore_patterns('charts', 'Chart.lock'))
        for path in ('charts/shared-stack', 'recipes/charts/sglang', 'recipes/charts/gguf-backend'):
            shutil.copytree(HERE/path, self.source/path, ignore=shutil.ignore_patterns('charts', 'Chart.lock', '__pycache__'))
        shutil.copy2(HERE/'package-charts.sh', self.source/'package-charts.sh')
        for guide in ('README.md', 'ADVANCED.md'):
            shutil.copy2(HERE/guide, self.source/guide)
        shutil.copy2(HERE/'recipes/index.json', self.source/'recipes/index.json')
        shutil.copy2(HERE/'recipes/NOTICE', self.source/'recipes/NOTICE')
        shutil.copy2(HERE/'recipes/NOTICE.deepseek-v4-flash', self.source/'recipes/NOTICE.deepseek-v4-flash')
        (self.source/'recipes/glm-5.3').mkdir()
        shutil.copy2(HERE/'recipes/glm-5.3/NOTICE', self.source/'recipes/glm-5.3/NOTICE')

    def snapshot(self):
        return {str(path.relative_to(self.source.parent)): hashlib.sha256(path.read_bytes()).hexdigest()
                for path in self.source.parent.rglob('*') if path.is_file()}

    def package(self, success=True):
        result = subprocess.run([str(self.source/'package-charts.sh'), '--output-dir', str(self.output)],
                                capture_output=True, text=True)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def test_packages_are_complete_and_catalog_points_to_real_archives(self):
        before = self.snapshot()
        self.output.mkdir()
        (self.output/'unrelated.txt').write_text('preserve this')
        self.package()
        self.assertEqual(self.snapshot(), before)
        self.assertEqual((self.output/'unrelated.txt').read_text(), 'preserve this')
        archives = list(self.output.glob('*.tgz'))
        self.assertEqual(len(archives), 3)
        packaged = {}
        for path in archives:
            with tarfile.open(path) as archive:
                member = next(item for item in archive.getmembers() if item.name.count('/') == 1 and item.name.endswith('/Chart.yaml'))
                chart = yaml.safe_load(archive.extractfile(member).read())
                packaged[chart['name']] = chart
                if chart['name'] == 'llm-shared-stack':
                    dependencies = {yaml.safe_load(archive.extractfile(item).read())['name']
                                    for item in archive.getmembers() if '/charts/' in item.name and item.name.endswith('/Chart.yaml')}
                    self.assertTrue({'pylon-operator', 'llm-gateway-stack', 'helm-nvcf-llm-api-gateway',
                                     'helm-nvcf-llm-request-router'} <= dependencies)
        index = json.loads((self.output/'index.json').read_text())
        for recipe in index['recipes']:
            for profile in recipe['profiles']:
                chart = profile['deployment']['chart']
                self.assertEqual(packaged[chart['name']]['version'], chart['version'])
                self.assertTrue((self.output/chart['archive']).is_file())
                self.assertEqual(chart['localPath'], chart['archive'])
                guide = profile['deployment']['guide'].split('#', 1)[0]
                self.assertEqual((self.output/guide).read_bytes(), (self.source/pathlib.Path(guide).name).read_bytes())
        sums = (self.output/'SHA256SUMS').read_text().splitlines()
        self.assertEqual(len(sums), 6 + sum(recipe['licenseNotice'] is not None for recipe in index['recipes']))
        for recipe in index['recipes']:
            notice = self.output/'notices'/recipe['id']/'NOTICE'
            self.assertEqual(notice.is_file(), recipe['licenseNotice'] is not None)
        self.assertEqual((self.output/'notices/deepseek-v4-flash/NOTICE').read_bytes(),
                         (self.source/'recipes/NOTICE.deepseek-v4-flash').read_bytes())
        self.assertNotEqual((self.output/'notices/deepseek-v4-flash/NOTICE').read_bytes(),
                            (self.source/'recipes/NOTICE').read_bytes())
        for line in sums:
            digest, relative = line.split(maxsplit=1)
            self.assertEqual(hashlib.sha256((self.output/relative).read_bytes()).hexdigest(), digest)

    def test_catalog_references_survive_moving_package_without_checkout(self):
        self.package()
        relocated = self.root/'separate-location'/'distribution'
        relocated.parent.mkdir()
        shutil.move(str(self.output), relocated)
        shutil.rmtree(self.source.parents[2])
        index = json.loads((relocated/'index.json').read_text())
        for recipe in index['recipes']:
            if recipe['licenseNotice']:
                self.assertTrue((relocated/recipe['licenseNotice']).is_file())
            for profile in recipe['profiles']:
                deployment = profile['deployment']
                chart = relocated/deployment['chart']['localPath']
                self.assertTrue(chart.is_relative_to(relocated))
                with tarfile.open(chart) as archive:
                    self.assertIn(deployment['chart']['name'] + '/Chart.yaml', archive.getnames())
                guide = relocated/deployment['guide'].split('#', 1)[0]
                self.assertTrue(guide.is_relative_to(relocated))
                self.assertTrue(guide.is_file())

    def test_catalog_metadata_is_preserved_except_packaged_paths(self):
        source = json.loads((self.source/'recipes/index.json').read_text())
        self.package()
        packaged = json.loads((self.output/'index.json').read_text())
        for original, actual in zip(source['recipes'], packaged['recipes']):
            for original_profile, actual_profile in zip(original['profiles'], actual['profiles']):
                source_guide = original_profile['deployment']['guide']
                target_guide = actual_profile['deployment']['guide']
                self.assertEqual(source_guide.partition('#')[1:], target_guide.partition('#')[1:])
                actual_profile['deployment']['guide'] = source_guide
                actual_profile['deployment']['chart']['localPath'] = original_profile['deployment']['chart']['localPath']
        self.assertEqual(packaged, source)

    def test_invalid_guide_path_fails_without_publishing(self):
        path = self.source/'recipes/index.json'
        index = json.loads(path.read_text())
        profile = next(recipe['profiles'][0] for recipe in index['recipes'] if recipe['profiles'])
        profile['deployment']['guide'] = '../../private.md'
        path.write_text(json.dumps(index))
        result = self.package(success=False)
        self.assertIn('Invalid deployment guide path', result.stderr)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_missing_guide_fails_without_publishing(self):
        (self.source/'README.md').unlink()
        result = self.package(success=False)
        self.assertIn('Missing deployment guide', result.stderr)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_model_packages_exclude_generated_bytecode_and_preserve_runtime_source(self):
        runtime_files = {'sglang': 'runtime.py', 'gguf-backend': 'automatic.py'}
        for chart, filename in runtime_files.items():
            source = self.source/'recipes/charts'/chart/'files'/filename
            py_compile.compile(str(source), doraise=True)
            py_compile.compile(str(source), cfile=str(source.with_suffix('.pyc')), doraise=True)
            self.assertTrue(list(source.parent.rglob('*.pyc')))
        self.package()
        for path in self.output.glob('*.tgz'):
            with tarfile.open(path) as archive:
                names = archive.getnames()
                self.assertFalse(any('__pycache__' in name.split('/') or name.endswith('.pyc') for name in names), path.name)
                for chart, filename in runtime_files.items():
                    source = self.source/'recipes/charts'/chart
                    metadata = yaml.safe_load((source/'Chart.yaml').read_text())
                    if path.name == metadata['name'] + '-' + metadata['version'] + '.tgz':
                        member = metadata['name'] + '/files/' + filename
                        self.assertEqual(archive.extractfile(member).read(), (source/'files'/filename).read_bytes())

    def test_existing_artifact_is_not_overwritten_and_no_partial_publish(self):
        self.output.mkdir()
        (self.output/'SHA256SUMS').write_text('existing build')
        before = self.snapshot()
        result = self.package(success=False)
        self.assertIn('Output already exists:', result.stderr)
        self.assertEqual((self.output/'SHA256SUMS').read_text(), 'existing build')
        self.assertEqual(list(self.output.iterdir()), [self.output/'SHA256SUMS'])
        self.assertEqual(self.snapshot(), before)

    def test_stale_catalog_fails_without_publishing(self):
        path = self.source/'recipes/index.json'
        index = json.loads(path.read_text())
        chart = next(recipe['profiles'][0]['deployment']['chart'] for recipe in index['recipes'] if recipe['profiles'])
        chart['version'] = '0.0.1-stale'
        chart['archive'] = chart['name'] + '-0.0.1-stale.tgz'
        path.write_text(json.dumps(index))
        result = self.package(success=False)
        self.assertIn('Recipe index is stale', result.stderr)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_missing_candidate_notice_does_not_fall_back_to_qwen_terms(self):
        (self.source/'recipes/NOTICE.deepseek-v4-flash').unlink()
        result = self.package(success=False)
        self.assertIn('Missing model notice for deepseek-v4-flash', result.stderr)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_invalid_notice_path_fails_without_publishing(self):
        path = self.source/'recipes/index.json'
        index = json.loads(path.read_text())
        next(recipe for recipe in index['recipes'] if recipe['licenseNotice'])['licenseNotice'] = '../NOTICE'
        path.write_text(json.dumps(index))
        result = self.package(success=False)
        self.assertIn('Invalid model notice path', result.stderr)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_missing_catalog_fails_before_packaging(self):
        (self.source/'recipes/index.json').unlink()
        result = self.package(success=False)
        self.assertIn('recipe catalog recipes/index.json is missing', result.stderr)
        self.assertEqual(list(self.output.iterdir()), [])


if __name__ == '__main__':
    unittest.main()
