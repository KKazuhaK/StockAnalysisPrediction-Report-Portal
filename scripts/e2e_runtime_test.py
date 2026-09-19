"""Regression checks for the container-backed end-to-end harness."""
import unittest
from unittest.mock import patch, MagicMock

from e2e_runtime import restart_container, check_spa, unused_totp_step


class RuntimeTests(unittest.TestCase):
    def test_totp_before_and_after_boundary(self):
        self.assertEqual(unused_totp_step({100}, now=3029), 101)
        self.assertEqual(unused_totp_step({100}, now=3030), 101)


    @patch('e2e_runtime.urllib.request.urlopen')
    @patch('e2e_runtime.subprocess.run')
    def test_restart_waits_for_container_readiness(self, run, urlopen):
        restart_container('rp-smoke', 'http://localhost:18790')
        run.assert_called_once_with(['docker', 'restart', 'rp-smoke'], check=True, timeout=60)
        urlopen.assert_called_once_with('http://localhost:18790/healthz', timeout=2)

    @patch('e2e_runtime.urllib.request.urlopen')
    def test_accepts_shell_and_built_assets(self, urlopen):
        responses = []
        for body, content_type in [
            (b'<div id="root"></div><script src="/assets/app.js"></script>'
             b'<link rel="stylesheet" href="/assets/app.css">', 'text/html'),
            (b'console.log("loaded")', 'text/javascript'),
            (b'body { color: black; }', 'text/css'),
        ]:
            response = MagicMock()
            response.__enter__.return_value = response
            response.read.return_value = body
            response.headers.get_content_type.return_value = content_type
            responses.append(response)
        urlopen.side_effect = responses
        check_spa('http://localhost:18790')
        self.assertEqual(urlopen.call_count, 3)

    @patch('e2e_runtime.time.sleep')
    @patch('e2e_runtime.urllib.request.urlopen')
    @patch('e2e_runtime.subprocess.run')
    def test_restart_retries_readiness(self, run, urlopen, sleep):
        from urllib.error import URLError
        urlopen.side_effect = [URLError('starting'), MagicMock()]
        restart_container('rp-smoke', 'http://localhost:18790')
        self.assertEqual(urlopen.call_count, 2)
        sleep.assert_called_once_with(1)

    @patch('e2e_runtime.urllib.request.urlopen')
    def test_rejects_html_fallback_for_missing_javascript(self, urlopen):
        page = MagicMock()
        page.read.return_value = b'<div id="root"></div><script src="/assets/app.js"></script>'
        page.headers.get_content_type.return_value = 'text/html'
        urlopen.return_value.__enter__.return_value = page
        with self.assertRaisesRegex(RuntimeError, 'content type'):
            check_spa('http://localhost:18790')

    @patch('e2e_runtime.urllib.request.urlopen')
    def test_rejects_placeholder_page(self, urlopen):
        page = MagicMock()
        page.read.return_value = b'frontend not built'
        urlopen.return_value.__enter__.return_value = page
        with self.assertRaisesRegex(RuntimeError, 'root'):
            check_spa('http://localhost:18790')


if __name__ == '__main__':
    unittest.main()
