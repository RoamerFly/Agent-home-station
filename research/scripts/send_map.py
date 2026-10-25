import json,re
f=json.load(open('research/evidence/main-functions.json'))
by=[(int(x['Entry'],16),int(x['End'],16),x['Name']) for x in f]
for file in ['research/evidence/send-pack-objdump.txt','research/evidence/send-msg-objdump.txt']:
 print('\n',file)
 for line in open(file,errors='ignore'):
  m=re.search(r'^\s*([0-9a-f]+):.*\bcall\s+([0-9a-f]+)',line)
  if m:
   src,t=map(lambda x:int(x,16),m.groups()); z=next((n for a,b,n in by if a<=t<b),'?')
   print(hex(src),'->',hex(t),z)
