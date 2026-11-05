"""Run the production Engine against a configured model using synthetic state only."""
import argparse,os,subprocess,json,hashlib
from pathlib import Path

PROGRESS_FIELDS = ('requested_cases', 'attempted_cases', 'remaining_cases',
                   'provider_blocked', 'provider_block', 'continuation',
                   'assessment_complete', 'model_assessment_complete',
                   'model_quality_gate_passed', 'release_gate_passed')


def report_status(data, fields):
 return {key: data.get(key) for key in dict.fromkeys((*fields, *(key for key in PROGRESS_FIELDS if key in data)))}


def refuse_provider_blocked_resume(path):
 if path.exists():
  data = json.loads(path.read_text(encoding='utf8'))
  if data.get('provider_blocked'):
   raise SystemExit('provider-blocked checkpoint requires explicit phase continuation; preserve completed generation, do not automatically skip or regenerate it')
def production_manifest(root):
 # go list reports the files selected for this exact GOOS/GOARCH, including
 # embedded assets and repository dependencies. Tests and external module caches
 # are excluded; go.mod/go.sum fix their versions. No configuration is read.
 packages=['./internal/'+p for p in ('companion','agenthost','deepseek','mcptools')]
 raw=subprocess.check_output(['go','list','-deps','-json',*packages],cwd=root,text=True,encoding='utf8')
 decoder=json.JSONDecoder();files={};used=[]
 while raw.strip():
  package,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
  directory=Path(package.get('Dir',''))
  if not directory.is_relative_to(root):continue
  used.append(package['ImportPath'])
  for field in ('GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','FFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles'):
   for name in package.get(field,[]):
    source=directory/name;files[source.relative_to(root).as_posix()]=hashlib.sha256(source.read_bytes()).hexdigest()
 for name in ('go.mod','go.sum'):files[name]=hashlib.sha256((root/name).read_bytes()).hexdigest()
 digest=hashlib.sha256(''.join(name+'\0'+files[name]+'\n' for name in sorted(files)).encode()).hexdigest()
 return {'sha256':digest,'method':'go-list-deps-selected-production-files-v1','packages':sorted(used),'files':dict(sorted(files.items())),'go':subprocess.check_output(['go','version'],cwd=root,text=True).strip()}
def main():
 p=argparse.ArgumentParser();p.add_argument('--config',type=Path,required=True);p.add_argument('--report',type=Path,required=True);p.add_argument('--limit',type=int);p.add_argument('--group');p.add_argument('--behavior');p.add_argument('--resume',action='store_true');p.add_argument('--timeout-seconds',type=int,default=28800);a=p.parse_args()
 if a.limit is not None and not 1<=a.limit<=240:raise SystemExit('limit must select 1..240 catalogue cases')
 if a.timeout_seconds<=0:raise SystemExit('timeout must be positive')
 config=json.loads(a.config.read_text(encoding='utf8'))
 if not all(config.get(k) for k in ('base_url','model','key_file')):raise SystemExit('config requires base_url, model, key_file; do not copy keys into JSON')
 a.report=a.report.resolve();a.report.parent.mkdir(parents=True,exist_ok=True)
 if a.resume:refuse_provider_blocked_resume(a.report)
 root=Path(__file__).resolve().parents[1]
 manifest=production_manifest(root);manifest_path=a.report.with_suffix('.source-manifest.json')
 if a.resume and manifest_path.exists() and json.loads(manifest_path.read_text(encoding='utf8'))['sha256']!=manifest['sha256']:raise SystemExit('production source changed; use a new report, do not resume')
 manifest_path.write_text(json.dumps(manifest,indent=2),encoding='utf8')
 env=dict(os.environ,WECHAT_DEEP_REPAIR_MODEL_CONFIG=str(a.config.resolve()),WECHAT_DEEP_REPAIR_MODEL_REPORT=str(a.report),WECHAT_DEEP_REPAIR_SOURCE_MANIFEST=str(manifest_path))
 if a.limit is not None:env['WECHAT_DEEP_REPAIR_MODEL_LIMIT']=str(a.limit)
 if a.group:env['WECHAT_DEEP_REPAIR_MODEL_GROUP']=a.group
 if a.behavior:env['WECHAT_DEEP_REPAIR_MODEL_BEHAVIOR']=a.behavior
 if a.resume:env['WECHAT_DEEP_REPAIR_RESUME']='1'
 code=subprocess.run(['go','test','./internal/companion','-run','^TestDeepRepairRealModelEvaluation$','-count=1','-v','-timeout',str(a.timeout_seconds)+'s'],cwd=root,env=env).returncode
 if a.report.exists():
  data=json.loads(a.report.read_text(encoding='utf8'))
  print(json.dumps(report_status(data, ('version','model','cases','passed','required_response_cases','responded','rubric_method','wechat_sends')),ensure_ascii=False))
 raise SystemExit(code)
if __name__=='__main__':main()
