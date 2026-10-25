"""Read only the reported animation request, associated events and state."""
import json,pathlib
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
activity=json.loads((root/'admin/activity.json').read_text())
received=[x for x in activity if x.get('category')=='received'and '超兽武装'in x.get('content','')and '视频'in x.get('content','')]
traces={x.get('message_id')for x in received}
events=[{k:x.get(k)for k in ('time','event','category','content','reason','status','message_id','send_id','tool','target_url','tool_code','tool_stage','query')}for x in activity if x.get('message_id')in traces and x.get('message_id')]
queue=json.loads((root/'bot-state/messages-v2.json').read_text())
states=[{'message_id':x['event']['data'].get('messageId',x['event']['data'].get('message_id')),'phase':x['phase']}for x in queue.get('inbox',[])if any(x['event']['data'].get(k)in traces for k in ['message_id','messageId'])]
config=json.loads((root/'admin/config.json').read_text())
report={'events':events,'states':states,'video_permission':config['companion']['permissions']['send_video'],'max_bubbles':config['companion']['max_bubbles'],'provider':config['provider'],'model':config['model'],'max_tokens':config['max_tokens'],'legacy_raw_model_output_available':False}
(workspace/'research/evidence/reported-video-history.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
