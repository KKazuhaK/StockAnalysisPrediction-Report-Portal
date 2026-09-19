"""Runtime checks shared by the container smoke and functional tests."""
from html.parser import HTMLParser
import subprocess
import time
import urllib.error
import urllib.request


class SPAParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.root = False
        self.assets = {}

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        self.root |= attrs.get('id') == 'root'
        if tag == 'script' and attrs.get('src'):
            self.assets[attrs['src']] = {'text/javascript', 'application/javascript'}
        if tag == 'link' and attrs.get('rel') == 'stylesheet' and attrs.get('href'):
            self.assets[attrs['href']] = {'text/css'}


def check_spa(base):
    with urllib.request.urlopen(base + '/', timeout=20) as response:
        parser = SPAParser()
        parser.feed(response.read().decode())
    if not parser.root or not parser.assets:
        raise RuntimeError('SPA shell is missing its root or built assets')
    for path, types in parser.assets.items():
        if not path.startswith('/assets/'):
            raise RuntimeError(f'Unexpected SPA asset path: {path}')
        with urllib.request.urlopen(base + path, timeout=20) as response:
            if response.headers.get_content_type() not in types:
                raise RuntimeError(f'Wrong content type for SPA asset: {path}')
            if not response.read():
                raise RuntimeError(f'Empty SPA asset: {path}')
    print(f'PASS SPA shell and {len(parser.assets)} built assets')


def restart_container(container, base):
    subprocess.run(['docker', 'restart', container], check=True, timeout=60)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(base + '/healthz', timeout=2):
                return
        except OSError:
            # A container being restarted resets the socket as often as it refuses the connection.
            # A reset is neither a URLError nor a TimeoutError — both of which are OSErrors — so
            # catching the family covers all three. Missing the reset failed the smoke test every
            # time it reached the restart layer, which is deterministic, not flaky.
            time.sleep(1)
    raise RuntimeError('Container did not become ready after restart')


def unused_totp_step(used, now=None):
    """Use a fresh current or next step within the server's clock-skew window."""
    current = int(time.time() if now is None else now) // 30
    for step in (current, current + 1):
        if step not in used:
            return step
    raise RuntimeError('No unused TOTP step available')
