#!/usr/bin/env python3
"""Real HTTP -> Hub -> WebSocket Agents -> two private engines, no production mutation."""
import json, pathlib, subprocess, sys, time, urllib.request, urllib.error, http.cookiejar
ROOT=pathlib.Path(sys.argv[1]).resolve()
RUN=sys.argv[2] if len(sys.argv)>2 else 'final'
assert RUN.replace('-','').isalnum()
assert str(ROOT).startswith('/tmp/arkade-migration-e2e-')
assert ROOT.joinpath('manifest-root').read_text().strip()==str(ROOT)
BASE='http://127.0.0.1:18191'
jar=http.cookiejar.CookieJar(); client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
def request(path, payload=None, method=None):
    data=None if payload is None else json.dumps(payload).encode()
    req=urllib.request.Request(BASE+path,data=data,method=method,headers={'Content-Type':'application/json'})
    try:
        with client.open(req,timeout=120) as r: return r.status,json.load(r)
    except urllib.error.HTTPError as e: return e.code,json.load(e)
def checked(path,payload=None,method=None,status=200):
    code,value=request(path,payload,method)
    assert code==status,(path,code,value)
    return value
def docker(side,*args):
    return subprocess.check_output(['docker','-H',f'unix://{ROOT}/{side}/docker.sock',*args],text=True).strip()
def task(name,opts):
    body={'target_agent_id':'arkmig-b','confirmation':name,'request_id':'e2e-'+name,**opts}
    result=checked(f'/api/agents/arkmig-a/stacks/{name}/migrate',body,status=202)['task']
    for i in range(300):
        result=checked(f'/api/agents/arkmig-a/stacks/{name}/operation-tasks/{result["task_id"]}')['task']
        if result['status'] in ('succeeded','failed','canceled'):
            ROOT.joinpath(name+'-task.json').write_text(json.dumps(result,ensure_ascii=False,indent=2))
            return result
        time.sleep(.25)
    raise AssertionError(('task timeout',result))
checked('/api/auth/login',{'username':'admin','password':ROOT.joinpath('admin-password').read_text().strip()})
for n in range(40):
    agents=checked('/api/agents')['agents']
    if len([a for a in agents if a.get('status')=='online'])==2: break
    time.sleep(1)
print('AGENTS',json.dumps(agents,ensure_ascii=False))
# Source projects use only private engine, never pull, no daemon network bridge needed.
def source(name):
    config=f'''services:
  fixture:
    image: alpine:3.20
    pull_policy: never
    network_mode: none
    command: ["sh", "-c", "sleep 3600"]
    volumes:
      - data:/data
volumes:
  data: {{}}
'''
    code,created=request('/api/agents/arkmig-a/stacks',{'name':name,'content':config,'env_content':'FIXTURE_SECRET=private-migration-test\n'})
    assert code==200,(code,created) # Fresh project only; never validate a previous task by accident.
    checked(f'/api/agents/arkmig-a/stacks/{name}/actions',{'action':'up'},status=200)
    cid=docker('a','ps','-q','--filter',f'label=com.docker.compose.project={name}')
    assert cid
    docker('a','exec',cid,'sh','-c','printf migration-sentinel-20261111 > /data/sentinel')
    before=docker('a','inspect',cid,'--format','{{.Id}} {{.State.StartedAt}} {{.RestartCount}}')
    return cid,before
default='migration-default-'+RUN
full='migration-full-'+RUN
cid,before=source(default)
result=task(default,{})
assert result['status']=='succeeded',result
assert not docker('b','ps','-aq','--filter','label=com.docker.compose.project='+default)
assert docker('a','inspect',cid,'--format','{{.Id}} {{.State.StartedAt}} {{.RestartCount}}')==before
assert docker('a','exec',cid,'cat','/data/sentinel')=='migration-sentinel-20261111'
assert result['migrate']['residual']['compose_written'] and not result['migrate']['residual']['started']
assert default+'_data' not in docker('b','volume','ls','--format','{{.Name}}').splitlines()
checked(f'/api/agents/arkmig-b/stacks/{default}/config')
# Explicit opt-in copies volume data and starts target, never touches source lifecycle.
cid,before=source(full)
result=task(full,{'include_volumes':True,'start_after':True})
assert result['status']=='succeeded',result
target=docker('b','ps','-q','--filter','label=com.docker.compose.project='+full)
assert target and docker('b','exec',target,'cat','/data/sentinel')=='migration-sentinel-20261111'
assert docker('a','inspect',cid,'--format','{{.Id}} {{.State.StartedAt}} {{.RestartCount}}')==before
assert docker('a','exec',cid,'cat','/data/sentinel')=='migration-sentinel-20261111'
# Existing target must remain untouched. New id means a genuine new attempt, not idempotent reuse.
code,conflict=request(f'/api/agents/arkmig-a/stacks/{full}/migrate',{'target_agent_id':'arkmig-b','confirmation':full,'request_id':'e2e-conflict-'+RUN})
assert code==202,(code,conflict)
conflict_id=conflict['task']['task_id']
for i in range(100):
    conflict=checked(f'/api/agents/arkmig-a/stacks/{full}/operation-tasks/{conflict_id}')['task']
    if conflict['status']=='failed': break
    time.sleep(.1)
assert conflict['status']=='failed' and '同名' in conflict.get('error',''),conflict
assert docker('b','ps','-q','--filter','label=com.docker.compose.project='+full)==target
assert docker('b','exec',target,'cat','/data/sentinel')=='migration-sentinel-20261111'
ROOT.joinpath('e2e-success.json').write_text(json.dumps({'default':True,'volumes_and_start':True,'source_unchanged':True,'target_collision_rejected':True},indent=2))
print('COMPOSE_MIGRATION_E2E_PASS')
