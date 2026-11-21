"""Reconcile installed WeChat-host tools after a running release is updated.

Only previously installed, reviewed WeChat runtime packages are considered.
Each package upgrade preserves configuration and never expands old grants.
"""

import argparse
import http.cookiejar
import json
from pathlib import Path
import subprocess
import sys
import urllib.request


WECHAT_RUNTIME_TOOLS = frozenset({
    'document-workshop', 'media-understanding', 'music',
    'project-development', 'rvc', 'share-reader', 'speech',
    'web-search', 'yue',
})


def installed_tool_ids(release):
    private = release / 'data/applications/wechat/legacy-admin'
    password = next((path.read_text(encoding='utf-8-sig').strip() for path in
                     (private / 'admin-password', private / 'admin-token') if path.is_file()), None)
    if not password:
        raise RuntimeError('local_admin_not_ready')
    base = 'http://127.0.0.1:8080'

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None

    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect())
    login = urllib.request.Request(base + '/api/login',
        data=json.dumps({'password': password}).encode(),
        headers={'Origin': base, 'Content-Type': 'application/json'})
    with opener.open(login, timeout=20) as response:
        if not json.load(response).get('authenticated'):
            raise RuntimeError('local_admin_login_rejected')
    with opener.open(urllib.request.Request(base + '/api/plugin-packages',
                     headers={'Origin': base}), timeout=20) as response:
        rows = json.load(response)['installed']
    return sorted(row['package']['id'] for row in rows
                  if not row['removed'] and row['package']['id'] in WECHAT_RUNTIME_TOOLS)


def sync(release):
    release = release.resolve(strict=True)
    ids = installed_tool_ids(release)
    helper = Path(__file__).with_name('update_installed_public_tool.py')
    for tool in ids:
        subprocess.run([sys.executable, str(helper), '--release', str(release),
                        '--tool', tool], check=True, stdin=subprocess.DEVNULL)
    return ids


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    args = parser.parse_args()
    ids = sync(args.release)
    print(json.dumps({'installed_wechat_tools_checked': ids}, ensure_ascii=False))


if __name__ == '__main__':
    main()
