"""Read public player metadata without credentials; never submit a WeChat send."""
import gzip,json,pathlib,re,urllib.request,urllib.parse
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
def get(url):
    with client.open(urllib.request.Request(url,headers={'User-Agent':'wechat-bot-web-tool/1.0','Accept-Encoding':'gzip'}),timeout=15)as r:
        b=r.read(2<<20)
        if r.headers.get('Content-Encoding')=='gzip':b=gzip.decompress(b)
        return b.decode('utf8','replace')
report={}
for bvid in ['BV1GPNr6JEs8','BV1qP4y187Lf']:
    try:
        body=get('https://www.bilibili.com/video/'+bvid+'/')
        info=re.search(r'window\.__playinfo__\s*=\s*(\{.*?\})\s*</script>',body)
        state=re.search(r'window\.__INITIAL_STATE__\s*=\s*(\{.*?\});',body)
        metadata=json.loads(state.group(1))if state else{}
        video=metadata.get('videoData',{});cid=video.get('cid')
        data=json.loads(info.group(1)).get('data',{})if info else{}
        if cid and not data:data=json.loads(get('https://api.bilibili.com/x/player/playurl?'+urllib.parse.urlencode({'bvid':bvid,'cid':cid,'qn':32,'fnval':0,'fourk':0}))).get('data',{})
        report[bvid]={'title':video.get('title'),'cid':cid,'has_playinfo':bool(info),'player_keys':list(data),'durl_count':len(data.get('durl',[])),'dash':bool(data.get('dash')),'duration_ms':data.get('timelength')}
        if data.get('durl'):
            u=data['durl'][0]['url'];parsed=urllib.parse.urlparse(u)
            report[bvid]['direct_host']=parsed.hostname;report[bvid]['direct_ext']=pathlib.PurePosixPath(parsed.path).suffix
    except Exception as e:report[bvid]={'error':type(e).__name__}
workspace=pathlib.Path(__file__).resolve().parents[2];(workspace/'research/evidence/bili-public-video-probe.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
