"""Compare public Bing search variants for the reported public video query."""
import concurrent.futures,json,pathlib,urllib.request,urllib.parse,xml.etree.ElementTree as ET
q='超兽武装 动画 片段 视频'
urls=[('default','https://www.bing.com/search?'+urllib.parse.urlencode({'format':'rss','q':q})),('cn','https://cn.bing.com/search?'+urllib.parse.urlencode({'format':'rss','q':q})),('market','https://www.bing.com/search?'+urllib.parse.urlencode({'format':'rss','q':q,'mkt':'zh-CN','setlang':'zh-Hans','cc':'cn'})),('quoted','https://cn.bing.com/search?'+urllib.parse.urlencode({'format':'rss','q':'"超兽武装" 动画 片段 视频','mkt':'zh-CN'}))]
def get(item):
    name,url=item
    try:
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}));req=urllib.request.Request(url,headers={'User-Agent':'wechat-bot-web-tool/1.0'})
        with opener.open(req,timeout=15)as response:body=response.read(2<<20)
        root=ET.fromstring(body);items=[{'title':x.findtext('title'),'link':x.findtext('link')}for x in root.findall('./channel/item')[:5]]
        return {'variant':name,'items':items,'matched_title':any('超兽武装'in x.get('title','')for x in items)}
    except Exception as e:return {'variant':name,'error':type(e).__name__}
with concurrent.futures.ThreadPoolExecutor(max_workers=4)as pool:results=list(pool.map(get,urls))
workspace=pathlib.Path(__file__).resolve().parents[2]
(workspace/'research/evidence/video-search-variants.json').write_text(json.dumps(results,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(results,ensure_ascii=False))
