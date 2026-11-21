"""Upgrade an explicitly selected installed tool from the deployed publictools.

Run after deploying/restarting release. Keeps grants, configuration and secrets;
verifies the installed digest instead of confusing catalog staging with upgrade.
No chat processing or messaging endpoints are invoked.
"""
import argparse
import hashlib
import http.cookiejar
import json
import io
import zipfile
from pathlib import Path
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--tool', required=True)
    args = parser.parse_args()
    release = args.release.resolve(strict=True)
    candidates = []
    for manifest in (release / 'publictools').glob('*/plugin.json'):
        package = json.loads(manifest.read_text(encoding='utf-8-sig'))
        if package.get('id') == args.tool:
            candidates.append((manifest, package))
    if len(candidates) != 1:
        raise RuntimeError('expected_one_deployed_tool')
    manifest, package = candidates[0]
    digest = hashlib.sha256(manifest.read_bytes()).hexdigest()
    private = release / 'data/applications/wechat/legacy-admin'
    password = next((p.read_text(encoding='utf-8-sig').strip() for p in
                     (private / 'admin-password', private / 'admin-token') if p.is_file()), None)
    if not password:
        raise RuntimeError('local_admin_not_ready')
    base = 'http://127.0.0.1:8080'
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect())
    csrf = ''
    def request(path, body=None, content_type='application/json'):
        headers = {'Origin': base, 'Content-Type': content_type, 'X-CSRF-Token': csrf}
        data = body if isinstance(body, bytes) else json.dumps(body).encode() if body is not None else None
        with opener.open(urllib.request.Request(base + path, data=data, headers=headers), timeout=90) as r:
            return json.load(r)
    csrf = request('/api/login', {'password': password})['csrf']
    path = '/api/plugin-packages/' + args.tool
    before = request(path)
    installed = before['installed']
    if installed['digest'] == digest:
        print(json.dumps({'tool': args.tool, 'state': 'already_current', 'digest': digest}))
        return
    if not set(package['permissions']).issubset(installed['package']['permissions']):
        raise RuntimeError('permission_declarations_changed_review_required')
    permissions = [name for name in installed['permissions'] if name in package['permissions']]
    # Tool roots also contain models/runtimes. Import only declared code files;
    # do not copy those resources into the installed execution snapshot.
    archive = io.BytesIO()
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as z:
        z.writestr('plugin.json', manifest.read_bytes())
        for name, expected in package['files'].items():
            source = (manifest.parent / name).resolve(strict=True)
            if not source.is_relative_to(manifest.parent.resolve()) or source.is_symlink():
                raise RuntimeError('package_member_outside_tool')
            content = source.read_bytes()
            if hashlib.sha256(content).hexdigest() != expected:
                raise RuntimeError('package_member_digest_changed')
            z.writestr(name, content)
    prepared = request('/api/plugin-packages/import', archive.getvalue(), 'application/zip')
    if prepared['digest'] != digest:
        raise RuntimeError('deployed_package_changed')
    enabled = installed['enabled']
    if enabled:
        request(path + '/runtime', {'action': 'stop', 'revision': installed['revision']})
    try:
        request('/api/plugin-packages/install', {'token': prepared['token'], 'digest': digest,
                'permissions': permissions, 'confirm': True})
    finally:
        # Restore prior enable intent even when installation rejects a schema
        # change. The store itself preserves the old package/config on failure.
        current = request(path)['installed']
        if enabled and not current['enabled']:
            request(path + '/runtime', {'action': 'enable_on_demand', 'revision': current['revision']})
    after = request(path)
    if (after['installed']['digest'] != digest or before['configuration'] != after['configuration']
            or after['installed']['permissions'] != permissions):
        raise RuntimeError('installed_digest_or_configuration_mismatch')
    print(json.dumps({'tool': args.tool, 'state': 'updated', 'digest': digest,
                      'enabled': after['installed']['enabled'], 'configuration_preserved': True,
                      'permissions': permissions}))


if __name__ == '__main__':
    main()
