#!/usr/bin/env python3
"""Destructive ONLY inside an existing isolated migration manifest; never run by import.

Lead runs: python3 compose-migration-e2e-failures.py /tmp/arkade-migration-e2e-...
Leaves uniquely named fixtures and JSON evidence for inspection; no cleanup/deletion.
"""
import http.cookiejar
import json
import pathlib
import re
import secrets
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:18191"
ROOT = None
CLIENT = None


def verify_root():
    if ROOT is None or not re.fullmatch(r"/tmp/arkade-migration-e2e-[A-Za-z0-9._-]+", str(ROOT)):
        raise RuntimeError("refusing non-isolated root")
    if ROOT.is_symlink() or ROOT.resolve() != ROOT:
        raise RuntimeError("root must be canonical and not a symlink")
    manifest = ROOT / "manifest-root"
    if manifest.is_symlink() or manifest.read_text().strip() != str(ROOT):
        raise RuntimeError("isolated manifest mismatch")
    for side in ("a", "b"):
        socket = ROOT / side / "docker.sock"
        if socket.resolve() != socket or socket.is_symlink() or not stat.S_ISSOCK(socket.stat().st_mode):
            raise RuntimeError("private engine socket missing or redirected")


def docker(side, *args):
    # Every invocation rechecks the manifest, including all mutating calls.
    verify_root()
    if side not in ("a", "b"):
        raise RuntimeError("unknown engine side")
    return subprocess.check_output(
        ["docker", "-H", f"unix://{ROOT}/{side}/docker.sock", *args],
        text=True, timeout=120,
    ).strip()


def request(path, payload=None, method=None):
    verify_root()  # HTTP writes must obey the same isolation boundary as Docker.
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        response = CLIENT.open(req, timeout=120)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        return response.status, json.loads(raw) if raw else None


def checked(path, payload=None, method=None, status=200):
    code, body = request(path, payload, method)
    assert code == status, (path, code, body)
    return body


def source(name):
    config = '''services:
  fixture:
    image: alpine:3.20
    pull_policy: never
    network_mode: none
    command: ["sh", "-c", "sleep 3600"]
    volumes:
      - data:/data
volumes:
  data: {}
'''
    # Unique names must be fresh: never accept 409/reuse a prior fixture.
    checked('/api/agents/arkmig-a/stacks', {'name': name, 'content': config})
    checked(f'/api/agents/arkmig-a/stacks/{name}/actions', {'action': 'up'})
    cid = docker('a', 'ps', '-q', '--filter', f'label=com.docker.compose.project={name}')
    assert cid and '\n' not in cid, (name, cid)
    sentinel = 'source-sentinel-' + name
    docker('a', 'exec', cid, 'sh', '-c', 'printf %s "$1" > /data/sentinel', 'fixture', sentinel)
    before = docker('a', 'inspect', cid, '--format', '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}')
    return cid, before, sentinel


def unchanged_source(cid, before, sentinel):
    assert docker('a', 'inspect', cid, '--format', '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}') == before
    assert docker('a', 'exec', cid, 'cat', '/data/sentinel') == sentinel


def migrate(name):
    result = checked(f'/api/agents/arkmig-a/stacks/{name}/migrate', {
        'target_agent_id': 'arkmig-b', 'confirmation': name,
        'request_id': 'failure-' + name, 'include_volumes': True, 'start_after': False,
    }, status=202)['task']
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        result = checked(f'/api/agents/arkmig-a/stacks/{name}/operation-tasks/{result["task_id"]}')['task']
        if result['status'] in ('succeeded', 'failed', 'canceled'):
            (ROOT / (name + '-task.json')).write_text(json.dumps(result, ensure_ascii=False, indent=2))
            assert result['status'] == 'failed', result
            return result
        time.sleep(.25)
    raise AssertionError(('migration timeout', result))


def marker_container(name, volume, marker):
    assert volume not in docker('b', 'volume', 'ls', '--format', '{{.Name}}').splitlines()
    docker('b', 'volume', 'create', volume)
    cid = docker('b', 'run', '-d', '--pull=never', '--network=none', '--name', name,
                 '-v', volume + ':/data', 'alpine:3.20', 'sh', '-c', 'sleep 3600')
    docker('b', 'exec', cid, 'sh', '-c', 'printf %s "$1" > /data/marker', 'fixture', marker)
    return cid


def existing_volume_case(prefix):
    name = prefix + '-volume'
    cid, before, sentinel = source(name)
    marker = 'preexisting-target-volume-' + name
    target = marker_container(name + '-holder', name + '_data', marker)
    target_before = docker('b', 'inspect', target, '--format', '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}')
    result = migrate(name)
    assert '卷' in result.get('error', '') and any(word in result['error'] for word in ('已存在', '已有')), result
    residual = result['migrate']['residual']
    assert residual['volumes_restored'] == 0 and not residual['compose_written'] and not residual['started'], result
    assert not residual.get('uncertain', False), result
    # Images may already have been imported before the volume collision; report it honestly.
    assert residual['images_imported'] >= 0, result
    assert docker('b', 'exec', target, 'cat', '/data/marker') == marker
    assert docker('b', 'inspect', target, '--format', '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}') == target_before
    assert not docker('b', 'ps', '-aq', '--filter', f'label=com.docker.compose.project={name}')
    status, _ = request(f'/api/agents/arkmig-b/stacks/{name}/config')
    assert status == 404, ('target compose unexpectedly written', status)
    unchanged_source(cid, before, sentinel)
    return result


def external_target_case(prefix):
    name = prefix + '-external'
    cid, before, sentinel = source(name)
    marker = 'external-target-marker-' + name
    target = marker_container(name + '-holder', name + '_outside', marker)
    # Give a manually-created running container the real Compose project label.
    # It is deliberately NOT imported through Hub and has no managed config.
    external = docker('b', 'run', '-d', '--pull=never', '--network=none', '--name', name + '-external',
                      '--label', 'com.docker.compose.project=' + name,
                      '--label', 'com.docker.compose.service=external',
                      '-v', name + '_outside:/data', 'alpine:3.20', 'sh', '-c', 'sleep 3600')
    external_before = docker('b', 'inspect', external, '--format', '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}')
    result = migrate(name)
    assert '同名' in result.get('error', ''), result
    residual = result['migrate']['residual']
    assert residual['images_imported'] == 0 and residual['volumes_restored'] == 0, result
    assert not residual['compose_written'] and not residual['started'] and not residual.get('uncertain', False), result
    assert docker('b', 'exec', target, 'cat', '/data/marker') == marker
    assert docker('b', 'inspect', external, '--format', '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}') == external_before
    assert name + '_data' not in docker('b', 'volume', 'ls', '--format', '{{.Name}}').splitlines()
    status, _ = request(f'/api/agents/arkmig-b/stacks/{name}/config')
    assert status == 404, ('external project got managed config', status)
    unchanged_source(cid, before, sentinel)
    return result


def main():
    global ROOT, CLIENT
    if len(sys.argv) != 2:
        raise SystemExit('usage: compose-migration-e2e-failures.py ISOLATED_ROOT')
    supplied = pathlib.Path(sys.argv[1])
    if supplied.is_symlink():
        raise RuntimeError('root symlink refused')
    ROOT = supplied.resolve()
    verify_root()
    jar = http.cookiejar.CookieJar()
    CLIENT = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    checked('/api/auth/login', {'username': 'admin', 'password': (ROOT / 'admin-password').read_text().strip()})
    for attempt in range(40):
        agents = checked('/api/agents')['agents']
        if {'arkmig-a', 'arkmig-b'} <= {a['id'] for a in agents if a.get('status') == 'online'}:
            break
        time.sleep(1)
    assert {'arkmig-a', 'arkmig-b'} <= {a['id'] for a in agents if a.get('status') == 'online'}, agents
    prefix = 'migration-fail-' + secrets.token_hex(5)
    volume = existing_volume_case(prefix)
    external = external_target_case(prefix)
    verify_root()
    evidence = {'existing_volume_rejected': True, 'external_target_rejected': True,
                'source_unchanged': True, 'target_markers_unchanged': True,
                'prefix': prefix, 'volume_task': volume, 'external_task': external}
    (ROOT / ('e2e-failures-' + prefix + '.json')).write_text(json.dumps(evidence, ensure_ascii=False, indent=2))
    print('COMPOSE_MIGRATION_FAILURES_E2E_PASS', prefix)


if __name__ == '__main__':
    main()
