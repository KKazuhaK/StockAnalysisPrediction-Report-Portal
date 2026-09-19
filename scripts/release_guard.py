"""Fail-closed release checks. All network reads must succeed before publication."""
import argparse
import json
import os
import re
import subprocess
import time


def gh_json(endpoint):
    return json.loads(subprocess.check_output(['gh', 'api', endpoint], text=True))


def image_digest(image):
    result = subprocess.run(['docker', 'buildx', 'imagetools', 'inspect', image],
                            capture_output=True, text=True, timeout=120)
    if result.returncode:
        # Authentication, transport and rate-limit failures are never evidence of absence.
        if re.search(r'manifest unknown|: not found(?:\s|$)', result.stderr, re.I) and not re.search(
                r'unauthorized|denied|timeout|429|403', result.stderr, re.I):
            return None
        raise RuntimeError(f'Cannot inspect {image}: {result.stderr}')
    match = re.search(r'^Digest:\s+(sha256:[a-f0-9]{64})\s*$', result.stdout, re.M)
    if not match:
        raise RuntimeError(f'Registry returned no digest for {image}')
    return match[1]


def validate_metadata(meta, tag, image, commit):
    if any(meta.get(k) != v for k, v in [('tag', tag), ('image', image), ('commit', commit)]):
        raise ValueError('Release metadata does not match tag, repository and commit')
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', meta.get('digest', '')):
        raise ValueError('Release metadata has no valid image digest')
    return meta['digest']


def ci_ready(jobs):
    required = {'go-test', 'web typecheck + build', 'workflow lint',
                'docker image (font gate + smoke)', 'go race full (other packages)'}
    required.update(f'go race full ({i}/4)' for i in range(1, 5))
    passed = {j['name'] for j in jobs if j.get('conclusion') == 'success'}
    return required <= passed


def wait_ci(repo, sha):
    deadline = time.monotonic() + 1800
    while time.monotonic() < deadline:
        runs = gh_json(f'repos/{repo}/actions/workflows/test.yml/runs?head_sha={sha}&per_page=100')['workflow_runs']
        runs = [r for r in runs if r['event'] in ('push', 'workflow_dispatch')]
        if not runs:
            raise RuntimeError('No full test run for this commit; dispatch test.yml on its branch first')
        run = max(runs, key=lambda r: r['id'])
        if run['status'] == 'completed':
            jobs = gh_json(f'repos/{repo}/actions/runs/{run["id"]}/jobs?per_page=100')['jobs']
            if run['conclusion'] != 'success' or not ci_ready(jobs):
                raise RuntimeError(f'Full CI did not pass: {run["html_url"]}')
            print(f'Verified full CI: {run["html_url"]}')
            return
        print(f'Waiting for full CI: {run["html_url"]}', flush=True)
        time.sleep(15)
    raise RuntimeError('Timed out waiting for full CI')


def expected_assets(tag):
    return {f'report-portal_{tag}_{platform}.{ext}' for platform, ext in (
        ('linux_amd64', 'tar.gz'), ('linux_arm64', 'tar.gz'),
        ('darwin_amd64', 'tar.gz'), ('darwin_arm64', 'tar.gz'),
        ('windows_amd64', 'zip'), ('windows_arm64', 'zip'))} | {'SHA256SUMS.txt', 'release-metadata.json'}


def read_state(repo):
    pages = json.loads(subprocess.check_output(
        ['gh', 'api', f'repos/{repo}/actions/variables?per_page=100', '--paginate', '--slurp'], text=True))
    variables = {v['name']: v['value'] for page in pages for v in page['variables']}
    for output, name in [('current_latest', 'CHANNEL_LATEST_TAG'), ('current_beta', 'CHANNEL_BETA_TAG'),
                         ('latest_override', 'CHANNEL_LATEST_OVERRIDE'), ('beta_override', 'CHANNEL_BETA_OVERRIDE')]:
        value = variables.get(name, '')
        if '\n' in value or '\r' in value:
            raise ValueError('Channel state must be a single line')
        print(f'{output}={value}')


def guard_draft(repo, tag):
    # Listing distinguishes a missing release from all transport/authentication failures.
    pages = json.loads(subprocess.check_output(
        ['gh', 'api', f'repos/{repo}/releases?per_page=100', '--paginate', '--slurp'], text=True))
    release = next((r for page in pages for r in page if r['tag_name'] == tag), None)
    if release is not None and not release['draft']:
        raise RuntimeError('Release is already published; cut a new version')


def verify_target(repo, tag, image):
    release = gh_json(f'repos/{repo}/releases/tags/{tag}')
    if release['draft']:
        raise RuntimeError('Cannot promote a draft release')
    if not expected_assets(tag) <= {a['name'] for a in release['assets']}:
        raise RuntimeError('Release assets are incomplete')
    ref = gh_json(f'repos/{repo}/git/ref/tags/{tag}')['object']
    if ref['type'] != 'tag':
        raise RuntimeError('Release tag must be annotated')
    commit = gh_json(f'repos/{repo}/git/tags/{ref["sha"]}')['object']
    if commit['type'] != 'commit':
        raise RuntimeError('Release tag must point directly to a commit')
    asset = next(a for a in release['assets'] if a['name'] == 'release-metadata.json')
    raw = subprocess.check_output(['gh', 'api', '-H', 'Accept: application/octet-stream',
                                   f'repos/{repo}/releases/assets/{asset["id"]}'])
    digest = validate_metadata(json.loads(raw), tag, image, commit['sha'])
    if image_digest(f'{image}:{tag}') != digest:
        raise RuntimeError('Fixed image tag no longer matches recorded digest')
    return digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['guard', 'wait-ci', 'verify-target', 'state', 'guard-draft'])
    parser.add_argument('value', nargs='?')
    parser.add_argument('--repo', default=os.environ.get('GITHUB_REPOSITORY'))
    parser.add_argument('--image')
    args = parser.parse_args()
    if args.command != 'state' and not args.value:
        parser.error('value is required for this command')
    if args.command == 'guard':
        if image_digest(args.value) is not None:
            raise RuntimeError('Fixed image already exists; do not rebuild or replace draft assets. '
                               'Use scripts/recover-release-metadata.py to verify and recover metadata.')
    elif args.command == 'state':
        read_state(args.repo)
    elif args.command == 'guard-draft':
        guard_draft(args.repo, args.value)
    elif args.command == 'wait-ci':
        wait_ci(args.repo, args.value)
    else:
        print(verify_target(args.repo, args.value, args.image))


if __name__ == '__main__':
    main()
