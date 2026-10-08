# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Build and distribute routing images independently of model recipes."""
import hashlib
import json
import os
import pathlib
import re
import subprocess
import tarfile
import tempfile
import time

HERE = pathlib.Path(__file__).resolve().parent
COMPONENTS = {'gateway': 'src/invocation-plane-services/llm-api-gateway',
              'router': 'src/libraries/rust/stargate', 'pylon': 'src/libraries/rust/stargate',
              'operator': 'src/compute-plane-services/pylon-operator'}
PLATFORMS = ('linux/arm64', 'linux/amd64')


class DockerUnavailableError(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def build_images(source, images, *, run, platform='linux/arm64', source_revision='unknown', operator_dockerfile=None):
    """Build named components into local image references using the caller's runner."""
    source = pathlib.Path(source)
    require(platform in PLATFORMS, 'Image platform must be linux/arm64 or linux/amd64.')
    require(images and set(images) <= COMPONENTS.keys(), 'Select known routing image components.')
    require(all((source/COMPONENTS[name]).is_dir() for name in images), 'Source checkout is missing required image sources.')
    try:
        run(['docker', 'info'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=15)
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        raise DockerUnavailableError('Start Docker, then retry the image build.') from None
    for name, image in images.items():
        command = ['docker', 'buildx', 'build', '--platform', platform, '--load', '--tag', image]
        if name in ('router', 'pylon'):
            command += ['--target', 'stargate-runtime' if name == 'router' else 'pylon-runtime', '--build-arg', 'CARGO_PROFILE=integration']
        if name == 'operator':
            revision = source_revision() if callable(source_revision) else source_revision
            command += ['-f', str(operator_dockerfile or HERE/'operator.Dockerfile'), '--build-arg', 'SOURCE_REVISION='+revision]
        run(command+[str(source/COMPONENTS[name])])


def export_images(images, archive, *, run):
    """Atomically save the selected image references into a private archive."""
    archive = pathlib.Path(archive)
    fd, temporary = tempfile.mkstemp(prefix='.'+archive.stem+'-', suffix='.tar', dir=archive.parent)
    os.close(fd)
    try:
        run(['docker', 'save', '--output', temporary] + list(images))
        os.chmod(temporary, 0o600)
        os.replace(temporary, archive)
    finally:
        pathlib.Path(temporary).unlink(missing_ok=True)
    print('Image archive:', archive)
    return archive


def import_images(archive, images, *, context, namespace, release, work, containerd, node_names,
                  control_node, helm_apply, run, output, save, archive_limit=1024**3,
                  platform='linux/arm64', archive_name='images.tar'):
    """Import an archive after the caller has authorized runtime access and bound the cluster.

    helm_apply(release, chart, values, wait=False) must apply only in namespace.
    The caller owns namespace creation and node identity checks.
    """
    require(platform in PLATFORMS, 'Image platform must be linux/arm64 or linux/amd64.')
    require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*', archive_name), 'Archive name must be a plain filename.')
    require(context and namespace and release, 'Image import requires an explicit context, namespace and release.')
    require(images and node_names, 'Select image references and import nodes.')
    work = pathlib.Path(work)
    kc = ['kubectl', '--context', context, '-n', namespace]
    hm = ['helm', '--kube-context', context, '-n', namespace]
    archive = pathlib.Path(archive).resolve(strict=True)
    require(archive.stat().st_size < archive_limit, 'The importer archive limit is '+str(archive_limit // 1024**3)+' GiB.')
    with tarfile.open(archive) as tar:
        manifest = json.load(tar.extractfile('manifest.json'))
    tags = {value for entry in manifest for value in entry.get('RepoTags', [])}
    for image in images:
        aliases = {image, image.removeprefix('docker.io/'), image.removeprefix('docker.io/library/')}
        require(bool(tags & aliases), 'Archive is missing the configured image: '+image)
    cfg = containerd
    require(cfg, 'No image importer was discovered. Use registry distribution or configure containerd import settings.')
    nodes = list(node_names)
    with archive.open('rb') as stream:
        archive_hash = hashlib.file_digest(stream, 'sha256').hexdigest()
    existing = json.loads(output(hm+['list', '--deployed', '--failed', '--pending', '--uninstalled',
                                          '--superseded', '--uninstalling', '--filter', '^'+re.escape(release)+'$', '-o', 'json']))
    require(all(item['chart'].startswith('pylon-image-loader-') for item in existing),
            'The image import release belongs to another chart.')
    prior_jobs = json.loads(output(kc+['get', 'jobs', '-o', 'json']))['items']
    owned = [job for job in prior_jobs if job['metadata'].get('annotations', {}).get('meta.helm.sh/release-name') == release]
    active = [job for job in owned if not any(c.get('type') in ('Complete', 'Failed') and c.get('status') == 'True'
                                              for c in job.get('status', {}).get('conditions', []))]
    attempt_path = work/'image-import-attempt.json'
    if active:
        previous = json.loads(attempt_path.read_text()) if attempt_path.exists() else {}
        require(previous.get('failed') and previous.get('release') == release
                and previous.get('context') == context and previous.get('namespace') == namespace
                and all(previous.get('jobs', {}).get(job['metadata']['name']) == job['metadata']['uid'] for job in active),
                'Another image import is active. Wait for it to finish.')
        status = json.loads(output(hm+['status', release, '-o', 'json']))
        require(status.get('version') == previous.get('revision'), 'Image import revision changed. Do not replace another attempt.')
    values = {'enabled': True, 'nodeNames': nodes, 'archiveNode': cfg.get('archiveNode', control_node),
              'archiveName': archive_name, 'platform': platform, 'archiveSha256': archive_hash,
              'archiveSizeLimit': str(archive_limit // 1024**3)+'Gi',
              'runAsUser': cfg.get('runAsUser', 1000), 'socketPath': cfg['socketPath']}
    for name in ('clientImage', 'serverImage'):
        if cfg.get(name):
            values[name] = cfg[name]
    helm_apply(release, HERE/'image-loader', values, wait=False)
    status = json.loads(output(hm+['status', release, '-o', 'json']))
    revision = status['version']
    require(isinstance(revision, int) and revision > 0, 'Image import release has no valid revision.')
    base = release+'-'+str(revision)
    names = [base+'-server']+[base+'-'+node for node in nodes]
    jobs = json.loads(output(kc+['get', 'jobs']+names+['-o', 'json']))['items']
    job_uids = {}
    for job in jobs:
        metadata = job['metadata']
        annotations = metadata.get('annotations', {})
        require(annotations.get('meta.helm.sh/release-name') == release
                and annotations.get('meta.helm.sh/release-namespace') == namespace,
                'Image import Job has different Helm ownership.')
        job_uids[metadata['name']] = metadata['uid']
    require(set(job_uids) == set(names), 'Image import Jobs differ from the installed revision.')
    server = base+'-server'
    try:
        deadline = time.monotonic() + 180
        while True:
            pods = json.loads(output(kc+['get', 'pods', '-l', 'job-name='+server, '-o', 'json']))['items']
            if pods:
                break
            require(time.monotonic() < deadline, 'Archive server pod was not created within 180 seconds.')
            time.sleep(2)
        require(len(pods) == 1 and not pods[0]['metadata'].get('deletionTimestamp'), 'Expected one active archive server pod.')
        pod = pods[0]
        require(any(owner.get('kind') == 'Job' and owner.get('uid') == job_uids[server]
                    for owner in pod['metadata'].get('ownerReferences', [])), 'Archive server pod has different Job ownership.')
        pod_name = pod['metadata']['name']
        run(kc+['wait', 'pod/'+pod_name, '--for=condition=Ready', '--timeout=180s'])
        ready = json.loads(output(kc+['get', 'pod', pod_name, '-o', 'json']))
        require(ready['metadata']['uid'] == pod['metadata']['uid'] and not ready['metadata'].get('deletionTimestamp')
                and ready.get('spec', {}).get('nodeName') == values['archiveNode'],
                'Archive server pod changed or has different placement.')
        upload = """import hashlib, os, sys
path, expected_hash, expected_size = sys.argv[1:]
expected_size = int(expected_size)
partial = path + '.upload'
size = 0
checksum = hashlib.sha256()
try:
    with open(partial, 'xb') as target:
        os.chmod(partial, 0o600)
        while chunk := sys.stdin.buffer.read(1024 * 1024):
            size += len(chunk)
            if size > expected_size:
                raise RuntimeError('Uploaded archive exceeds its expected size')
            checksum.update(chunk)
            target.write(chunk)
    if size != expected_size or checksum.hexdigest() != expected_hash:
        raise RuntimeError('Uploaded archive size or SHA-256 differs')
    os.replace(partial, path)
finally:
    if os.path.exists(partial):
        os.unlink(partial)
"""
        print('Uploading image archive through Kubernetes:', archive.stat().st_size, 'bytes')
        with archive.open('rb') as stream:
            run(kc+['exec', '-i', pod['metadata']['name'], '-c', 'server', '--', 'python3', '-c', upload,
                         '/images/'+archive_name, archive_hash, str(archive.stat().st_size)], stdin=stream, timeout=1200)
        run(kc+['wait', '--for=condition=complete', '--timeout=15m']+['job/'+name for name in names])
        completed = json.loads(output(kc+['get', 'jobs']+names+['-o', 'json']))['items']
        require({job['metadata']['name']: job['metadata']['uid'] for job in completed} == job_uids,
                'Image import Jobs changed during verification.')
    except BaseException:
        save(attempt_path, {'failed': True, 'release': release, 'revision': revision, 'jobs': job_uids,
                            'context': context, 'namespace': namespace})
        raise
    attempt_path.unlink(missing_ok=True)
    save(work/'evidence/image-import.json', {'release': release, 'revision': revision,
         'archiveSha256': archive_hash, 'nodeNames': nodes, 'jobs': job_uids})
    print('Images imported on:', ', '.join(nodes))
