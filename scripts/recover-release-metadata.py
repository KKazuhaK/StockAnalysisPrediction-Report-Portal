#!/usr/bin/env python3
"""Recover metadata after a successful image push without rebuilding or replacing binaries.

Usage: python3 scripts/recover-release-metadata.py OWNER/REPO vYYYY.W[.R]
Requires gh authentication and Docker registry access. Writes metadata only after both Linux
archive binaries match the corresponding immutable image binaries byte for byte.
"""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone
from release_guard import gh_json, image_digest


def recover(repo, tag):
    release = gh_json(f'repos/{repo}/releases/tags/{tag}')
    if not release['draft']:
        raise RuntimeError('Recovery is only allowed for a draft release')
    ref = gh_json(f'repos/{repo}/git/ref/tags/{tag}')['object']
    if ref['type'] != 'tag':
        raise RuntimeError('Expected an annotated release tag')
    obj = gh_json(f'repos/{repo}/git/tags/{ref["sha"]}')['object']
    if obj['type'] != 'commit':
        raise RuntimeError('Expected a tag pointing directly to a commit')
    commit = obj['sha']
    image = f'ghcr.io/{repo.lower()}'
    digest = image_digest(f'{image}:{tag}')
    if digest is None:
        raise RuntimeError('There is no fixed image to recover')
    immutable = f'{image}@{digest}'
    with tempfile.TemporaryDirectory(prefix='release-recovery-') as tmp:
        root = Path(tmp)
        subprocess.run(['gh', 'release', 'download', tag, '--repo', repo, '--dir', tmp,
                        '--pattern', 'SHA256SUMS.txt', '--pattern', f'report-portal_{tag}_linux_*.tar.gz'], check=True)
        sums = dict(line.split()[::-1] for line in (root/'SHA256SUMS.txt').read_text().splitlines())
        for arch in ('amd64', 'arm64'):
            name = f'report-portal_{tag}_linux_{arch}.tar.gz'
            archive = root/name
            if hashlib.sha256(archive.read_bytes()).hexdigest() != sums.get(name):
                raise RuntimeError(f'Archive checksum mismatch: {name}')
            with tarfile.open(archive) as tar:
                member = tar.getmember(f'report-portal_{tag}_linux_{arch}/report-portal')
                if not member.isfile():
                    raise RuntimeError('Archive binary must be a regular file')
                binary_hash = hashlib.sha256(tar.extractfile(member).read()).hexdigest()
            subprocess.run(['docker', 'pull', '--platform', f'linux/{arch}', immutable], check=True)
            container = subprocess.check_output(['docker', 'create', '--platform', f'linux/{arch}', immutable], text=True).strip()
            try:
                info = json.loads(subprocess.check_output(['docker', 'inspect', container]))[0]
                if info['Config']['Labels'].get('org.opencontainers.image.revision') != commit:
                    raise RuntimeError('Image revision does not match release tag')
                destination = root/f'image-{arch}'
                subprocess.run(['docker', 'cp', f'{container}:/app/report-portal', str(destination)], check=True)
                if hashlib.sha256(destination.read_bytes()).hexdigest() != binary_hash:
                    raise RuntimeError(f'Image and archive binaries differ for {arch}; cut a new version')
            finally:
                subprocess.run(['docker', 'rm', container], check=True)
        # Detect publication or tag movement during verification before writing anything.
        if not gh_json(f'repos/{repo}/releases/tags/{tag}')['draft']:
            raise RuntimeError('Release was published during verification')
        if gh_json(f'repos/{repo}/git/ref/tags/{tag}')['object'] != ref:
            raise RuntimeError('Release tag moved during verification')
        if image_digest(f'{image}:{tag}') != digest:
            raise RuntimeError('Fixed image moved during verification')
        metadata = root/'release-metadata.json'
        metadata.write_text(json.dumps(dict(tag=tag, commit=commit, image=image, digest=digest,
                                           recorded_at=datetime.now(timezone.utc).isoformat()))+'\n')
        subprocess.run(['gh', 'release', 'upload', tag, str(metadata), '--repo', repo, '--clobber'], check=True)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    recover(*sys.argv[1:])
