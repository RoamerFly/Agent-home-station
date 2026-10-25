import concurrent.futures,json,pathlib,re,urllib.parse,urllib.request
q='超兽武装 动画 片段 视频'
urls=[('sogou','https://www.sogou.com/web?'+urllib.parse.urlencode({'query':q})),('so','https://www.so.com/s?'+urllib.parse.urlencode({'q':q})),('bing-exact','https://www.bing.com/search?format=rss&q='+urllib.parse.quote('超兽武装',safe='')),('bili-search','https://search.bilibili.com/all?'+urllib.parse.urlencode({'keyword':q})),('bili-official','https://www.bilibili.com/video/BV1qP4y187Lf/')]
def probe(item):
    name,url=item
    try:
        client=urllib.request.build_opener(urllib.request.ProxyHandler({}));req=urllib.request.Request(url,headers={'User-Agent':'wechat-bot-web-tool/1.0'})
        with client.open(req,timeout=20)as response:body=response.read(2<<20).decode('utf8','replace');status=response.status
        
        if name in ['sogou','so','bili-search']:(pathlib.Path(__file__).resolve().parents[2]/('temp/'+name+'-search.html')).write_text(body,encoding='utf8')
        text=re.sub('<[^>]+>',' ',re.sub(r'(?is)<(script|style).*?</\1>','',body));text=re.sub(r'\s+',' ',text)
        return {'name':name,'status':status,'bytes':len(body),'text':text[:450],'matches':body.count('超兽武装'),'challenge':bool(re.search(r'(?i)anomaly|captcha|unusual traffic',body))}
    except Exception as e:return {'name':name,'error':type(e).__name__}
with concurrent.futures.ThreadPoolExecutor(max_workers=5)as p:results=list(p.map(probe,urls))
workspace=pathlib.Path(__file__).resolve().parents[2];(workspace/'research/evidence/video-search-fallback-probe.json').write_text(json.dumps(results,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(results,ensure_ascii=False))
