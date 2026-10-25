"""Authenticated local YuE/Comfy bridge. No source changes or downloads.
Run with --project <YuE app> --state <private directory> --token-file <file>.
The bridge persists jobs and never automatically resubmits interrupted work.
"""
import argparse, base64, concurrent.futures, hashlib, hmac, json, os, secrets, threading, time, urllib.request, urllib.error, subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

def singing_profile(config_path):
    if not config_path:return None
    path=Path(config_path)
    data=path.read_bytes();config=json.loads(data)
    if not config.get('enabled',False):return None
    if not all(isinstance(config.get(k),str) and config[k] for k in ('profile','python','runtime','reference','reference_sha256','source_revision')):raise ValueError('singing_voice_config_invalid')
    if not Path(config['python']).is_file() or not Path(config['reference']).is_file():raise ValueError('singing_voice_resources_missing')
    if hashlib.sha256(Path(config['reference']).read_bytes()).hexdigest()!=config['reference_sha256']:raise ValueError('singing_voice_reference_changed')
    return {'profile':config['profile'],'config_sha256':hashlib.sha256(data).hexdigest(),'python':config['python']}

def convert_singing_voice(config_path,expected,source,destination):
    current=singing_profile(config_path)
    if current is None or current['config_sha256']!=expected['config_sha256']:raise ValueError('singing_voice_configuration_changed')
    try:subprocess.run([current['python'],str(Path(__file__).with_name('singing_voice.py')),'--config',str(config_path),'--source',str(source),'--output',str(destination)],check=True,timeout=3600)
    except (subprocess.CalledProcessError,subprocess.TimeoutExpired):raise ValueError('singing_voice_conversion_failed') from None
    if not destination.is_file() or destination.stat().st_size<44:raise ValueError('singing_voice_output_missing')
    return destination

def apply_song_duration(graph,seconds):
    if isinstance(seconds,bool) or not isinstance(seconds,(int,float)) or not 10<=seconds<=600:raise ValueError('invalid_duration')
    found=False
    for node in graph.values():
        if node.get('class_type')=='YuE2GenerateMusic':
            node['inputs']['max_duration']=float(seconds);found=True
    if not found:raise ValueError('duration_control_missing')

def song_options(body):
    reference=body.get('reference_base64','')
    if not isinstance(reference,str):raise ValueError('invalid_reference')
    mode=body.get('mode') or ('cover' if reference else 'original')
    if mode not in ('original','cover'):raise ValueError('invalid_mode')
    if (mode=='cover')!=bool(reference):raise ValueError('cover_requires_reference')
    reference_mode=body.get('reference_mode','melody')
    if reference_mode not in ('melody','full'):raise ValueError('invalid_reference_mode')
    seed=body.get('seed')
    if seed is not None and (isinstance(seed,bool) or not isinstance(seed,int) or not 0<=seed<=2147483647):raise ValueError('invalid_seed')
    return mode,reference_mode,seed

def prepare_song_graph(workflow_dir,body,jid,input_dir):
    mode,reference_mode,seed=song_options(body)
    if seed is None:raise ValueError('seed_not_persisted')
    graph=json.loads((workflow_dir/('audio_yue2_music_cover.json' if mode=='cover' else 'audio_yue2_text_to_music.json')).read_text(encoding='utf-8'))
    target=body.get('duration_seconds',60);apply_song_duration(graph,target)
    # Preserve the user's supplied style/lyrics. Duration is controlled by the
    # real generation node, not by silently injecting a new short-song style.
    if mode=='cover':
        try:data=base64.b64decode(body['reference_base64'],validate=True)
        except Exception:raise ValueError('invalid_reference_encoding') from None
        if not 0<len(data)<=25*1024*1024:raise ValueError('reference_size_limit')
        input_dir.mkdir(parents=True,exist_ok=True)
        source=input_dir/('wechat-ref-'+jid+'.audio');source.write_bytes(data)
        name='wechat-ref-'+jid+'.wav'
        subprocess.run(['ffmpeg','-v','error','-nostdin','-y','-protocol_whitelist','file','-i',str(source),'-vn','-ar','44100','-ac','2','-t','600',str(input_dir/name)],check=True,timeout=90)
        graph['45']['inputs']['audio']=name
        graph['33:25']['inputs']['style']=body['style'];graph['33:25']['inputs']['lyrics']=body['lyrics']
        graph['33:25']['inputs']['mode']=reference_mode
        graph['33:41']['inputs']['mode']=reference_mode
    else:
        graph['33:36']['inputs']['value']=body['style'];graph['33:37']['inputs']['value']=body['lyrics']
    graph['33:34']['inputs']['seed']=seed
    graph['10']['inputs']['filename_prefix']='wechat-agent/'+jid
    return graph

def main():
    p=argparse.ArgumentParser();p.add_argument('--project',required=True);p.add_argument('--workflow-dir');p.add_argument('--state',required=True);p.add_argument('--token-file',required=True);p.add_argument('--port',type=int,default=6188);p.add_argument('--bind',default='127.0.0.1');p.add_argument('--singing-voice-config');args=p.parse_args()
    project=Path(args.project).resolve();state=Path(args.state).resolve();state.mkdir(parents=True,exist_ok=True)
    workflow_dir=Path(args.workflow_dir).resolve() if args.workflow_dir else project/'workflows_api'
    for filename in ('audio_yue2_music_cover.json','audio_yue2_text_to_music.json'):
        if not (workflow_dir/filename).is_file():raise ValueError('workflow_missing: '+filename)
    token=Path(args.token_file).read_text().strip();lock=threading.RLock();pool=concurrent.futures.ThreadPoolExecutor(max_workers=1)
    db=state/'jobs.json';jobs=json.loads(db.read_text()) if db.exists() else {}
    def save():
        tmp=db.with_suffix('.tmp');tmp.write_text(json.dumps(jobs,ensure_ascii=False),encoding='utf8');tmp.replace(db)
    for job in jobs.values():
        if job['state'] in ('queued','running'):job.update(state='needs_review',reason='bridge_restarted_no_replay')
    save()
    def api(route,body=None,timeout=20):
        req=urllib.request.Request('http://127.0.0.1:8188'+route,data=None if body is None else json.dumps(body).encode(),headers={'Content-Type':'application/json'})
        with urllib.request.urlopen(req,timeout=timeout) as r:
            data=r.read()
            return json.loads(data) if data else {}
    def run(jid,body):
        try:
            with lock:
                if jobs[jid]['state']=='cancelled':return
                jobs[jid]['state']='running';save()
            target=body.get('duration_seconds',60)
            graph=prepare_song_graph(workflow_dir,body,jid,project/'comfy/ComfyUI/input')
            submitted=api('/prompt',{'prompt':graph,'client_id':'wechat-yue-'+jid})
            prompt_id=submitted['prompt_id']
            with lock:jobs[jid]['prompt_id']=prompt_id;save()
            for _ in range(10800):
                history=api('/history/'+prompt_id).get(prompt_id)
                if history:
                    if history.get('status',{}).get('status_str')=='error':raise ValueError('comfy_execution_error')
                    files=[item for node in history.get('outputs',{}).values() for key,items in node.items() if isinstance(items,list) for item in items if isinstance(item,dict) and item.get('filename') and item.get('type')=='output']
                    audio=next((x for x in files if Path(x['filename']).suffix.lower() in ('.wav','.mp3','.flac','.ogg','.m4a')),None)
                    if not audio:raise ValueError('no_generated_audio')
                    from urllib.parse import urlencode
                    with urllib.request.urlopen('http://127.0.0.1:8188/view?'+urlencode(audio),timeout=60) as r:data=r.read(64*1024*1024+1)
                    if not 0<len(data)<=64*1024*1024:raise ValueError('output_size_limit')
                    output=state/(jid+Path(audio['filename']).suffix);output.write_bytes(data)
                    duration=float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','default=nw=1:nk=1',str(output)],timeout=30))
                    if duration>target+0.5:raise ValueError('generation_duration_exceeded')
                    # Remove only codec padding, never shorten a multi-minute result
                    # and pretend the generator followed the requested duration.
                    if duration>target:
                        adjusted=state/(jid+'-bounded.flac')
                        subprocess.run(['ffmpeg','-v','error','-y','-i',str(output),'-t',str(target),'-c:a','flac',str(adjusted)],check=True,timeout=60)
                        output=adjusted;duration=float(target)
                    voice=jobs[jid].get('singing_voice')
                    if voice:
                        with lock:jobs[jid]['phase']='converting_voice';save()
                        api('/free',{'unload_models':True,'free_memory':True})
                        output=convert_singing_voice(args.singing_voice_config,voice,output,state/(jid+'-character-voice.wav'))
                    with lock:jobs[jid].update(state='completed',phase='completed',file=output.name,bytes=output.stat().st_size,duration_seconds=duration,requested_seconds=target);save()
                    return
                time.sleep(1)
            raise ValueError('generation_deadline')
        except Exception as e:
            with lock:jobs[jid].update(state='failed',reason=str(e)[:120] if isinstance(e,ValueError) else type(e).__name__);save()
        finally:
            with lock:idle=not any(j['state'] in ('queued','running') for j in jobs.values())
            if idle:
                try:api('/free',{'unload_models':True,'free_memory':True})
                except Exception:pass
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def reply(self,status,value):
            data=json.dumps(value,ensure_ascii=False).encode();self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
        def authenticated(self):return hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+token)
        def do_GET(self):
            if not self.authenticated():return self.reply(401,{'error':'unauthorized'})
            if self.path=='/health':
                try:
                    api('/system_stats');voice=singing_profile(args.singing_voice_config)
                    return self.reply(200,{'ready':True,'bridge_version':3,'capabilities':['original','reference_cover','seed','reference_mode']+(['singing_voice_conversion'] if voice else []),'singing_voice_profile':voice['profile'] if voice else None,'cover_boundary':'ABC-conditioned resynthesis; optional local singing conversion after generation; no guarantee of exact source accompaniment or reference timbre'})
                except Exception:return self.reply(200,{'ready':False})
            parts=self.path.strip('/').split('/');jid=parts[1] if len(parts)>1 else ''
            with lock:job=dict(jobs.get(jid,{}))
            if not job:return self.reply(404,{'error':'not_found'})
            if len(parts)==3 and parts[2]=='audio':
                if job['state']!='completed':return self.reply(409,{'error':'not_completed'})
                file=state/job['file'];data=file.read_bytes();self.send_response(200);self.send_header('Content-Type','audio/flac' if file.suffix=='.flac' else 'audio/wav');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data);return
            job.pop('file',None);return self.reply(200,job)
        def do_POST(self):
            if not self.authenticated():return self.reply(401,{'error':'unauthorized'})
            size=int(self.headers.get('Content-Length','0'))
            if size<=0 or size>36*1024*1024:return self.reply(413,{'error':'size_limit'})
            try:body=json.loads(self.rfile.read(size))
            except Exception:return self.reply(400,{'error':'invalid_json'})
            if self.path!='/jobs':return self.reply(404,{'error':'not_found'})
            if not isinstance(body.get('style'),str) or not isinstance(body.get('lyrics'),str) or len(body['style'])>2000 or len(body['lyrics'])>6000:return self.reply(400,{'error':'invalid_song_input'})
            target=body.get('duration_seconds',60)
            if isinstance(target,bool) or not isinstance(target,(int,float)) or not 10<=target<=600:return self.reply(400,{'error':'invalid_duration'})
            try:mode,reference_mode,seed=song_options(body)
            except ValueError as e:return self.reply(400,{'error':str(e)})
            try:voice=singing_profile(args.singing_voice_config)
            except Exception:return self.reply(503,{'error':'singing_voice_not_ready'})
            # Keep a request fingerprint before assigning an optional seed. A
            # reused key cannot silently change the reference, lyrics or take.
            fingerprint=hashlib.sha256(json.dumps(body,sort_keys=True,ensure_ascii=False).encode()).hexdigest()
            key=self.headers.get('Idempotency-Key','')
            # Resolve an existing idempotent result even while the backend is
            # offline. A new job requires a live generator, before persistence.
            with lock:
                for old in jobs.values():
                    if key and old.get('key')==key:
                        if old.get('request_hash') and old['request_hash']!=fingerprint:return self.reply(409,{'error':'idempotency_conflict'})
                        return self.reply(200,{k:v for k,v in old.items() if k!='file'})
            try:api('/system_stats',timeout=5)
            except Exception:return self.reply(503,{'error':'backend_unavailable'})
            with lock:
                for old in jobs.values():
                    if key and old.get('key')==key:
                        if old.get('request_hash') and old['request_hash']!=fingerprint:return self.reply(409,{'error':'idempotency_conflict'})
                        return self.reply(200,{k:v for k,v in old.items() if k!='file'})
                if len(jobs)>=500:return self.reply(409,{'error':'job_history_limit'})
                if sum(j['state'] in ('queued','running') for j in jobs.values())>=3:return self.reply(429,{'error':'queue_full'})
                if seed is None:seed=secrets.randbelow(2**31)
                body['seed']=seed
                jid=secrets.token_hex(16);jobs[jid]={'id':jid,'state':'queued','key':key,'created':time.time(),'reference_used':mode=='cover','mode':mode,'reference_mode':reference_mode,'seed':seed,'request_hash':fingerprint,'requested_seconds':target,'singing_voice':{k:v for k,v in voice.items() if k!='python'} if voice else None};save();pool.submit(run,jid,body)
            self.reply(202,{'id':jid,'state':'queued','seed':seed,'mode':mode,'reference_used':mode=='cover','singing_voice_profile':voice['profile'] if voice else None})
    # Windows/WSL access: token required on every endpoint. No remote code/API.
    ThreadingHTTPServer((args.bind,args.port),Handler).serve_forever()

if __name__=='__main__':main()
