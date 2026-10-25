"""Read only messages relevant to the user's reported switch-to-file request."""
import json,pathlib,sqlite3
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
activity=json.loads((root/'admin/activity.json').read_text())
matches=[{k:x.get(k)for k in ('time','message_id','content')}for x in activity if x.get('thread_id')=='50583054216@chatroom'and x.get('category')=='received'and any(p in x.get('content','')for p in ['不用发语音','不用语音','音乐文件'])]
print(json.dumps(matches,ensure_ascii=False))
