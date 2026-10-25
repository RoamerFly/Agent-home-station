import json
from pathlib import Path
r=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
c=json.loads((r/'admin/config.json').read_text())
print(json.dumps({'group_personas':[{'name':g.get('name'),'persona':g.get('persona')}for g in c['group_rules']], 'provider':c['provider'],'model':c['model'],'system_prompt':c['system_prompt'],'character':c['companion']},ensure_ascii=False,indent=2))
