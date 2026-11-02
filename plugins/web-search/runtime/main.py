"""Public search/read plugin. Network I/O is mediated by the authorized host.

No socket access, environment credentials, WeChat API or self-install behavior.
Only the standard library is needed by the isolated Python runtime.
"""
import base64
import hashlib
from html.parser import HTMLParser
import json
import re
import sys
import urllib.parse
import xml.etree.ElementTree as ET

FRAME_LIMIT = 8 * 1024 * 1024


class PageText(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.hidden = 0
        self.title_depth = 0
        self.title = []
        self.parts = []

    def handle_starttag(self, tag, attrs):
        if tag in ("script", "style", "noscript", "template"):
            self.hidden += 1
        if tag == "title":
            self.title_depth += 1

    def handle_endtag(self, tag):
        if tag in ("script", "style", "noscript", "template"):
            self.hidden = max(0, self.hidden - 1)
        if tag == "title":
            self.title_depth = max(0, self.title_depth - 1)

    def handle_data(self, text):
        if self.hidden:
            return
        (self.title if self.title_depth else self.parts).append(text)


def clean(text):
    return " ".join(text.split())


def public_url(value):
    p = urllib.parse.urlsplit(value)
    if p.scheme not in ("http", "https") or not p.hostname or p.username or p.password or p.fragment:
        raise ValueError("invalid_public_url")
    return value


def integer(args, name, default, minimum):
    value = args.get(name, default)
    if type(value) is not int or value < minimum:
        raise ValueError("invalid_" + name)
    return value


def read_page(get, url, args):
    maximum = integer(args, "max_bytes", 2 * 1024 * 1024, 1)
    timeout = integer(args, "timeout_seconds", 20, 1)
    response = get(public_url(url), maximum, timeout)
    if not response.get("ok"):
        return None, {"ok": False, "error": response.get("error", "public_read_failed")}
    body = base64.b64decode(response["body_base64"], validate=True)
    if len(body) > maximum:
        raise ValueError("host_response_exceeds_requested_bytes")
    return (body, response.get("content_type", ""), public_url(response["url"])), None


def fetch(args, get):
    allowed = {"url", "offset", "limit", "expected_sha256", "max_bytes", "timeout_seconds"}
    if set(args) - allowed or not isinstance(args.get("url"), str):
        raise ValueError("invalid_read_arguments")
    page, failure = read_page(get, args["url"], args)
    if failure:
        return failure
    body, content_type, url = page
    if content_type and not any(t in content_type.lower() for t in ("text/", "json", "xml")):
        raise ValueError("not_text")
    encoding = "utf-8"
    for item in content_type.split(";")[1:]:
        if item.strip().lower().startswith("charset="):
            encoding = item.split("=", 1)[1].strip().strip('"')
    text = body.decode(encoding, errors="replace")
    title = ""
    if "html" in content_type.lower() or not content_type:
        parser = PageText()
        parser.feed(text)
        text, title = clean(" ".join(parser.parts)), clean(" ".join(parser.title))
    if not text.strip():
        raise ValueError("no_readable_text")
    digest = hashlib.sha256(text.encode("utf8")).hexdigest()
    if args.get("expected_sha256") and args["expected_sha256"] != digest:
        raise ValueError("page_content_changed")
    offset, limit = integer(args, "offset", 0, 0), integer(args, "limit", 10000, 1)
    end = min(len(text), offset + limit)
    if offset > len(text):
        raise ValueError("offset_outside_page")
    return {"ok": True, "url": url, "title": title, "text": text[offset:end], "sha256": digest,
            "offset": offset, "requested_limit": limit, "returned_characters": end - offset,
            "total_characters": len(text), "next_offset": end if end < len(text) else None,
            "complete": end == len(text), "untrusted_external_content": True}


def discovery_url(source, related=""):
    if not isinstance(source, dict) or set(source) != {"id", "name", "type", "url", "kind", "enabled"}:
        raise ValueError("discovery_source_invalid")
    if (not isinstance(source["id"], str) or not 1 <= len(source["id"]) <= 64
            or not isinstance(source["name"], str) or not 1 <= len(source["name"]) <= 100
            or source["kind"] not in ("text", "voice", "video", "image") or source["enabled"] is not True
            or not isinstance(source["url"], str) or len(source["url"]) > 2048):
        raise ValueError("discovery_source_invalid")
    url = public_url(source["url"])
    parsed = urllib.parse.urlsplit(url)
    kind = source["type"]
    if kind == "bilibili":
        if parsed.netloc != "api.bilibili.com" or parsed.path not in ("/x/web-interface/popular", "/x/web-interface/ranking/v2"):
            raise ValueError("discovery_source_invalid")
    elif kind == "bing_images":
        if parsed.netloc != "www.bing.com" or parsed.path != "/HPImageArchive.aspx":
            raise ValueError("discovery_source_invalid")
    elif kind == "audio_catalog":
        if url != "https://incompetech.com/music/royalty-free/pieces.json":
            raise ValueError("discovery_source_invalid")
    elif kind != "rss":
        raise ValueError("discovery_source_invalid")
    if related:
        related = urllib.parse.urlsplit(public_url(related))
        match = re.fullmatch(r"/video/(BV[0-9A-Za-z]{10})/?", related.path)
        if kind != "bilibili" or related.hostname not in ("www.bilibili.com", "bilibili.com") or not match:
            raise ValueError("discovery_related_invalid")
        url = "https://api.bilibili.com/x/web-interface/archive/related?" + urllib.parse.urlencode({"bvid": match[1]})
    return url


def markup_text(value):
    parser = PageText()
    parser.feed(value)
    return clean(" ".join(parser.parts + parser.title))


def discover(args, get):
    if set(args) - {"source", "related_url", "offset", "limit"}:
        raise ValueError("invalid_discovery_arguments")
    source = args.get("source")
    related = args.get("related_url", "")
    if not isinstance(related, str):
        raise ValueError("discovery_related_invalid")
    url = discovery_url(source, related)
    offset, limit = integer(args, "offset", 0, 0), integer(args, "limit", 40, 1)
    page, failure = read_page(get, url, {"max_bytes": 2 * 1024 * 1024, "timeout_seconds": 12})
    if failure:
        return failure
    body, _, final_url = page
    items = []

    def add(title, link, summary="", published="", media="", kind=None, duration=0):
        if not isinstance(title, str) or not title.strip() or not isinstance(link, str):
            return
        try:
            public_url(link)
        except ValueError:
            return
        if media:
            media = urllib.parse.urljoin(source["url"], media)
            try:
                public_url(media)
            except ValueError:
                media = ""
        row = {"title": markup_text(title)[:400], "summary": markup_text(summary)[:900],
               "url": link, "kind": kind or source["kind"], "source": source["name"]}
        if media:
            row["media_url"] = media
        if published:
            row["published"] = str(published)
        if duration:
            row["duration_ms"] = duration
        items.append(row)

    if source["type"] == "bilibili":
        data = json.loads(body)
        if not isinstance(data, dict) or data.get("code") != 0:
            raise ValueError("discovery_feed_unavailable")
        rows = data.get("data")
        rows = rows.get("list") if isinstance(rows, dict) else rows
        if not isinstance(rows, list):
            raise ValueError("discovery_feed_unavailable")
        for row in rows:
            if not isinstance(row, dict) or not re.fullmatch(r"BV[0-9A-Za-z]{10}", str(row.get("bvid", ""))):
                continue
            duration = row.get("duration", 0)
            duration = duration * 1000 if type(duration) is int and duration > 0 else 0
            stamp = row.get("pubdate", 0)
            # Provider timestamps remain metadata, never proof of playback.
            if type(stamp) is int and stamp > 0:
                import datetime
                try:
                    stamp = datetime.datetime.fromtimestamp(stamp, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
                except (ValueError, OverflowError, OSError):
                    stamp = ""
            else:
                stamp = ""
            add(row.get("title", ""), "https://www.bilibili.com/video/" + row["bvid"] + "/", row.get("desc", ""), stamp, duration=duration)
    elif source["type"] == "bing_images":
        data = json.loads(body)
        for row in data.get("images", []):
            if isinstance(row, dict):
                add(row.get("title", ""), row.get("copyrightlink", ""), row.get("copyright", "") + "；来源图片，不代表自由版权。", media=urllib.parse.urljoin("https://www.bing.com", row.get("url", "")), kind="image")
    elif source["type"] == "audio_catalog":
        rows = json.loads(body)
        if not isinstance(rows, list):
            raise ValueError("catalog_invalid")
        for row in rows:
            filename = row.get("filename", "") if isinstance(row, dict) else ""
            if not isinstance(filename, str) or not filename.lower().endswith(".mp3") or "/" in filename or "\\" in filename:
                continue
            add(row.get("title", ""), "https://incompetech.com/music/royalty-free/", "Kevin MacLeod 器乐；按来源标注署名许可。" + row.get("description", ""), media="https://incompetech.com/music/royalty-free/mp3-royaltyfree/" + urllib.parse.quote(filename, safe=""), kind="voice")
        # Stable pages within one fetched catalogue; host rotates sources and
        # samples candidates. No hidden cache or provider fallback exists here.
    else:
        if b"<!DOCTYPE" in body.upper() or b"<!ENTITY" in body.upper():
            raise ValueError("discovery_feed_unavailable")
        root = ET.fromstring(body)

        def child(element, tag):
            return next((node for node in element if node.tag.rsplit("}", 1)[-1] == tag), None)

        def value(element, tag):
            node = child(element, tag)
            return "" if node is None else "".join(node.itertext())

        for entry in root.iter():
            tag = entry.tag.rsplit("}", 1)[-1]
            if tag not in ("item", "entry"):
                continue
            title, summary = value(entry, "title"), value(entry, "description") + " " + value(entry, "encoded")
            link, media, kind = value(entry, "link").strip(), "", source["kind"]
            stamp = value(entry, "pubDate")
            if tag == "entry":
                summary, stamp = value(entry, "summary") + " " + value(entry, "content"), value(entry, "published") or value(entry, "updated")
                for node in entry:
                    if node.tag.rsplit("}", 1)[-1] == "link":
                        if node.get("rel", "") in ("", "alternate"):
                            link = node.get("href", "")
                        elif node.get("rel") == "enclosure":
                            media = node.get("href", "")
            else:
                enclosure = child(entry, "enclosure")
                if enclosure is not None:
                    media = enclosure.get("url", "")
                    kind = {"audio": "voice", "video": "video", "image": "image"}.get(enclosure.get("type", "").split("/", 1)[0], kind)
            if kind == "image" and not media:
                image = re.search(r'<img\b[^>]*\bsrc\s*=\s*["\']([^"\']+)', summary, re.I)
                if image:
                    media = image[1]
            add(title, link, summary, stamp, media, kind)
    if not items:
        raise ValueError("discovery_feed_unavailable")
    if offset > len(items):
        raise ValueError("offset_outside_results")
    end = min(len(items), offset + limit)
    return {"ok": True, "source": final_url, "candidates": items[offset:end], "metadata_only": True,
            "offset": offset, "requested_limit": limit, "total_items": len(items), "next_offset": end if end < len(items) else None,
            "complete": end == len(items), "untrusted_external_content": True}


def public_search(args, get, bilibili_only=False):
    if set(args) - {"query", "count", "offset", "limit", "max_bytes", "timeout_seconds"}:
        raise ValueError("invalid_search_arguments")
    query = args.get("query")
    if not isinstance(query, str) or not query.strip():
        raise ValueError("query_required")
    count = integer(args, "count", 6, 1)
    term = query.strip() + (" site:bilibili.com/video" if bilibili_only else "")
    def matches(item):
        if not bilibili_only:
            return relevant(query, item)
        if not re.fullmatch(r"https://(?:www\.)?bilibili\.com/video/BV[A-Za-z0-9]{10}/?(?:\?[^#]*)?", item["url"]):
            return False
        qualifiers = {"原唱", "原曲", "试听", "音频", "下载", "音乐", "歌曲", "mv", "b站", "bilibili"}
        terms = [t for t in re.findall(r"[^\W_]+", query.casefold()) if t not in qualifiers]
        text = (item["title"] + " " + item["summary"]).casefold()
        return bool(terms) and all(t in text for t in terms)
    url = "https://www.bing.com/search?" + urllib.parse.urlencode({"q": term, "format": "rss"})
    page, failure = read_page(get, url, args)
    items, seen = [], set()
    source = url
    if not failure:
        body, _, source = page
        try:
            # ElementTree does not resolve external entities.
            root = ET.fromstring(body)
            for item in root.findall("./channel/item"):
                try:
                    link = public_url(clean(item.findtext("link", "")))
                except ValueError:
                    continue
                if link in seen:
                    continue
                seen.add(link)
                summary = PageText()
                summary.feed(item.findtext("description", ""))
                items.append({"url": link, "title": clean(item.findtext("title", "")), "summary": clean(" ".join(summary.parts))})
        except ET.ParseError:
            items = []
    items = [item for item in items if matches(item)]
    fallback = not items
    if fallback:
        secondary = "https://www.so.com/s?" + urllib.parse.urlencode({"q": term})
        page, failure = read_page(get, secondary, args)
        if failure:
            return failure
        body, _, source = page
        parser = SearchText()
        parser.feed(body.decode("utf8", errors="replace"))
        items = [item for item in parser.results if matches(item)]
    if not items:
        return {"ok": False, "error": "search_no_relevant_results"}
    items = items[:count]
    offset, limit = integer(args, "offset", 0, 0), integer(args, "limit", 8, 1)
    end = min(len(items), offset + limit)
    if offset > len(items):
        raise ValueError("offset_outside_results")
    return {"ok": True, "backend": "bilibili" if bilibili_only else "public", "query": query, "source": source, "items": items[offset:end],
            "fallback_used": fallback or bilibili_only,
            "requested_count": count, "effective_provider_count": len(items), "requested_limit": limit, "provider_items": len(items), "next_offset": end if end < len(items) else None,
            "provider_page_exhausted": end == len(items), "untrusted_external_content": True}


def deepseek_search(args, route):
    if set(args) - {"query", "count", "offset", "limit", "max_bytes", "timeout_seconds"}:
        raise ValueError("invalid_search_arguments")
    query = args.get("query")
    if not isinstance(query, str) or not query.strip():
        raise ValueError("query_required")
    count = integer(args, "count", 6, 1)
    timeout = integer(args, "timeout_seconds", 18, 1)
    maximum = integer(args, "max_bytes", 128 * 1024, 1)
    reply = route("deepseek-search", {"query": query.strip(), "count": count, "need_content": False}, maximum, timeout)
    if not isinstance(reply, dict) or set(reply) - {"results"} or not isinstance(reply.get("results"), list):
        raise ValueError("deepseek_search_response_invalid")
    items, seen = [], set()
    for result in reply["results"]:
        if not isinstance(result, dict) or set(result) - {"title", "url", "summary", "snippet"}:
            raise ValueError("deepseek_search_response_invalid")
        try:
            url = public_url(result.get("url", ""))
        except ValueError:
            continue
        if url in seen:
            continue
        title, summary = result.get("title", ""), result.get("summary", result.get("snippet", ""))
        if not isinstance(title, str) or not isinstance(summary, str):
            raise ValueError("deepseek_search_response_invalid")
        seen.add(url)
        items.append({"url": url, "title": clean(title), "summary": clean(summary)})
    offset, limit = integer(args, "offset", 0, 0), integer(args, "limit", 8, 1)
    if offset > len(items):
        raise ValueError("offset_outside_results")
    end = min(len(items), offset + limit)
    return {"ok": True, "backend": "deepseek", "query": query, "items": items[offset:end], "fallback_used": False,
            "requested_count": count, "effective_provider_count": count, "requested_limit": limit, "provider_items": len(items),
            "next_offset": end if end < len(items) else None, "provider_page_exhausted": end == len(items), "untrusted_external_content": True}


def bilibili_search(args, get):
    query = args.get("query")
    if not isinstance(query, str) or not query.strip():
        raise ValueError("query_required")
    count = integer(args, "count", 6, 1)
    offset, limit = integer(args, "offset", 0, 0), integer(args, "limit", 8, 1)
    page_size = 20
    number, start = divmod(offset, page_size)
    endpoint = "https://api.bilibili.com/x/web-interface/search/type?" + urllib.parse.urlencode(
        {"search_type": "video", "keyword": query.strip(), "page": number + 1, "page_size": page_size})
    page, failure = read_page(get, endpoint, args)
    if failure:
        return failure
    body, _, final = page
    data = json.loads(body)
    if final != endpoint or data.get("code") != 0 or not isinstance(data.get("data"), dict):
        return {"ok": False, "error": "bilibili_search_unavailable"}
    result = data["data"]
    rows = result.get("result") or []
    if not isinstance(rows, list) or result.get("page") != number + 1 or result.get("pagesize") != page_size:
        return {"ok": False, "error": "bilibili_search_unavailable"}
    total = min(count, result["numResults"], result["numPages"] * page_size)
    if offset > total:
        raise ValueError("offset_outside_results")
    window = rows[start:start + min(limit, total - offset)]
    items = []
    for row in window:
        bvid = row.get("bvid", "")
        if not re.fullmatch(r"BV[A-Za-z0-9]{10}", bvid):
            continue
        title = PageText()
        title.feed(row.get("title", ""))
        items.append({"url": "https://www.bilibili.com/video/" + bvid + "/",
                      "title": clean("".join(title.parts)), "summary": clean(row.get("author", ""))})
    end = offset + len(window)
    return {"ok": True, "backend": "bilibili", "query": query, "source": final, "items": items,
            "requested_count": count, "requested_limit": limit, "effective_provider_count": total,
            "provider_items": result["numResults"], "next_offset": end if window and end < total else None,
            "provider_page_exhausted": not window or end >= total, "untrusted_external_content": True}


def search(args, get, route=None, configuration=None):
    if args.get("source") == "bilibili":
        result = bilibili_search(args, get)
        if result.get("ok") and (result.get("items") or args.get("offset", 0) > 0):
            return result
        if not result.get("ok") and result.get("error") not in ("http_status", "bilibili_search_unavailable"):
            return result
        return public_search({k:v for k,v in args.items() if k != "source"}, get, bilibili_only=True)
    backend = (configuration or {}).get("search_backend", "public")
    if backend == "public":
        return public_search(args, get)
    if backend == "deepseek":
        if route is None:
            raise ValueError("deepseek_route_unavailable")
        return deepseek_search(args, route)
    raise ValueError("search_backend_not_configured")


def relevant(query, item):
    query = re.sub(r"\bsite:\S+", "", query.lower())
    quoted = re.search(r'["“《]([^"”》]{2,100})["”》]', query)
    tokens = re.findall(r"[^\W_]+", query)
    subject = quoted.group(1) if quoted else next((t for t in tokens if len(t) >= 2), "")
    norm = lambda s: "".join(c for c in s.lower() if c.isalnum())
    hay = norm(item["title"] + " " + item["summary"])
    categories = {"可爱动物": ("猫", "狗", "宠物", "动物", "海獭", "企鹅"), "日常趣事": ("趣事", "搞笑", "生活", "日常"), "好吃的": ("美食", "食谱", "烹饪", "小吃")}
    matched = not subject or norm(subject) in hay or any(term in hay for term in categories.get(subject, ()))
    stop = set("song songs music audio video videos lyrics official artist original online listen download bilibili bv mp com www search".split())
    return matched and all(word in hay for word in re.findall(r"[a-z]{2,}", query) if word not in stop)


class SearchText(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.heading, self.anchor, self.results, self.seen = False, None, [], set()

    def handle_starttag(self, tag, attrs):
        if tag in ("h2", "h3"):
            self.heading = True
        if tag == "a" and self.heading:
            values = dict(attrs)
            self.anchor = [values.get("data-mdurl") or values.get("href") or "", []]

    def handle_data(self, text):
        if self.anchor is not None:
            self.anchor[1].append(text)

    def handle_endtag(self, tag):
        if tag in ("h2", "h3"):
            self.heading = False
        if tag == "a" and self.anchor is not None:
            link, title = self.anchor[0], clean(" ".join(self.anchor[1]))
            self.anchor = None
            try:
                public_url(link)
            except ValueError:
                return
            if title and link not in self.seen and not urllib.parse.urlsplit(link).hostname.endswith("so.com"):
                self.seen.add(link)
                self.results.append({"url": link, "title": title, "summary": ""})


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.seq, self.configuration = incoming, outgoing, 0, {}

    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line:
            raise EOFError()
        if len(line.encode("utf8")) > FRAME_LIMIT or not line.endswith("\n"):
            raise ValueError("frame_limit")
        return json.loads(line)

    def write(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if len(raw.encode("utf8")) + 1 > FRAME_LIMIT:
            raise ValueError("frame_limit")
        self.outgoing.write(raw + "\n")
        self.outgoing.flush()

    def get(self, url, maximum, timeout):
        self.seq += 1
        key = "web-read-" + str(self.seq)
        self.write({"jsonrpc": "2.0", "id": key, "method": "wechat/http_read",
                    "params": {"url": url, "max_bytes": maximum, "timeout_seconds": timeout}})
        reply = self.read()
        if reply.get("jsonrpc") != "2.0" or reply.get("id") != key:
            raise ValueError("host_response_identity_mismatch")
        if "error" in reply:
            return {"ok": False, "error": "host_service_denied_or_failed"}
        return reply["result"]

    def route(self, route, body, maximum, timeout):
        self.seq += 1
        key = "web-route-" + str(self.seq)
        self.write({"jsonrpc": "2.0", "id": key, "method": "wechat/http_route",
                    "params": {"route": route, "body": body, "max_bytes": maximum, "timeout_seconds": timeout}})
        reply = self.read()
        if reply.get("jsonrpc") != "2.0" or reply.get("id") != key:
            raise ValueError("host_response_identity_mismatch")
        if "error" in reply:
            raise ValueError("host_service_denied_or_failed")
        if not isinstance(reply.get("result"), dict):
            raise ValueError("invalid_host_response")
        return reply["result"]

    def serve(self):
        initialized = False
        while True:
            try:
                req = self.read()
            except EOFError:
                return
            if "id" not in req:
                continue
            result = {}
            try:
                if req.get("jsonrpc") != "2.0":
                    raise ValueError("invalid_protocol")
                method = req.get("method")
                if method == "initialize":
                    self.configuration = req.get("params", {}).get("_meta", {}).get("wechat/configuration", {}).get("values", {})
                    if not isinstance(self.configuration, dict):
                        raise ValueError("invalid_configuration")
                    initialized = True
                    result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}}, "serverInfo": {"name": "web-search", "version": "0.3.1"}}
                elif method == "ping" and initialized:
                    pass
                elif method == "tools/call" and initialized:
                    params = req["params"]
                    context = params.get("_meta", {}).get("wechat/context", {})
                    if not all(context.get(k) for k in ("account", "thread", "requester", "purpose", "request_key")):
                        raise ValueError("host_context_required")
                    action = {"web.search": "search", "web.read": "read", "web.discover": "discover"}.get(params["name"])
                    if not action or not isinstance(params.get("arguments"), dict):
                        raise ValueError("unknown_capability_or_arguments")
                    if action == "search":
                        result = search(params["arguments"], self.get, self.route, self.configuration)
                    elif action == "discover":
                        result = discover(params["arguments"], self.get)
                    else:
                        result = fetch(params["arguments"], self.get)
                else:
                    raise ValueError("method_unavailable")
            except (ValueError, KeyError, TypeError, LookupError, ET.ParseError) as error:
                # No raw exceptions (may contain source text or local paths).
                code = str(error)
                result = {"ok": False, "error": code if re.fullmatch(r"[a-z_]{1,96}", code) else "invalid_request_or_page"}
            self.write({"jsonrpc": "2.0", "id": req["id"], "result": result})


if __name__ == "__main__":
    sys.stdin.reconfigure(encoding="utf8")
    sys.stdout.reconfigure(encoding="utf8")
    Wire(sys.stdin, sys.stdout).serve()
