"""Share-platform parsing, independent of the chat/WeChat process.

Network reads are supplied by the host. Resolved media locators are private
handoff data, not evidence that the video has been watched or delivered.
"""
import base64
import hashlib
from html.parser import HTMLParser
import ipaddress
import json
import re
from urllib.parse import parse_qs, urlencode, urljoin, urlsplit, urlunsplit

MAX_PAGE_BYTES = 4 * 1024 * 1024  # Host stdio response envelope boundary.
PHOTO = re.compile(r"^/(?:fw/photo|short-video)/([A-Za-z0-9]{8,40})/?$")
STATE = re.compile(r"window\.__APOLLO_STATE__\s*=\s*")
KUAI_HOSTS = {"v.m.chenzhongtech.com", "www.kuaishou.com", "kuaishou.com"}
BILI_HOSTS = {"www.bilibili.com", "bilibili.com"}
BILI_VIDEO = re.compile(r"^/video/(BV[A-Za-z0-9]{10})/?$")
BILI_SHORT = re.compile(r"^/[A-Za-z0-9]{3,32}/?$")
BILI_STATE = re.compile(r"window\.__INITIAL_STATE__\s*=\s*")


def public_url(raw):
    if not isinstance(raw, str) or not raw or len(raw) > 16384 or any(ord(c) < 33 for c in raw):
        raise ValueError("invalid_public_url")
    p = urlsplit(raw)
    if p.scheme not in ("http", "https") or not p.hostname or p.username is not None or p.password is not None or "\\" in raw:
        raise ValueError("invalid_public_url")
    # Validate port even when the platform permits explicit public ports.
    _ = p.port
    host = p.hostname.lower()
    if host in ("localhost", "localhost.localdomain") or host.endswith((".localhost", ".local", ".internal")):
        raise ValueError("invalid_public_url")
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        address = None
    if address is not None and not address.is_global:
        raise ValueError("invalid_public_url")
    return urlunsplit((p.scheme, p.netloc, p.path, p.query, ""))


def display_url(raw):
    p = urlsplit(public_url(raw))
    return urlunsplit((p.scheme, p.netloc, p.path, "", ""))


def integer(args, key, default, minimum):
    value = args.get(key, default)
    if type(value) is not int or value < minimum:
        raise ValueError("invalid_" + key)
    return value


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("share_ambiguous_metadata")
        result[key] = value
    return result


def bili_identity(raw):
    p = urlsplit(public_url(raw))
    if p.hostname not in BILI_HOSTS:
        return None
    match = BILI_VIDEO.fullmatch(p.path)
    if not match or p.port is not None:
        raise ValueError("video_page_unsupported")
    parts = parse_qs(p.query, keep_blank_values=True).get("p", ["1"])
    if len(parts) != 1 or not re.fullmatch(r"[1-9][0-9]*", parts[0]):
        raise ValueError("video_part_unavailable")
    return match[1], int(parts[0])


def bili_metadata(body, identity):
    marker = BILI_STATE.search(body)
    if not marker:
        raise ValueError("video_page_metadata_unavailable")
    try:
        data, _ = json.JSONDecoder(object_pairs_hook=unique_object).raw_decode(body[marker.end():])
        video = data["videoData"]
        bvid, part = identity
        cid, title, pages = video.get("cid"), video.get("title"), video.get("pages", [])
        if video.get("bvid") != bvid:
            raise ValueError("share_identity_conflict")
        if type(cid) is not int or cid <= 0 or not isinstance(title, str) or not title.strip() or not isinstance(pages, list):
            raise TypeError()
        if pages:
            if part > len(pages):
                raise ValueError("video_part_unavailable")
            page = pages[part - 1]
            selected, name = page.get("cid"), page.get("part", "")
            if type(selected) is not int or selected <= 0 or not isinstance(name, str):
                raise TypeError()
            if part == 1 and cid != selected:
                raise ValueError("share_identity_conflict")
            cid = selected
            if part > 1 and name:
                title += " · " + name
        elif part != 1:
            raise ValueError("video_part_unavailable")
        if len(title.encode("utf8")) > 4096:
            raise TypeError()
        return cid, title
    except (KeyError, TypeError, AttributeError, json.JSONDecodeError):
        raise ValueError("video_page_metadata_unavailable") from None


def bili_playback(body):
    try:
        result = json.loads(body, object_pairs_hook=unique_object)
        data = result["data"]
        preview = data.get("is_preview", False)
        if type(result.get("code")) is not int or result["code"] != 0 or type(preview) not in (bool, int) or preview not in (False, 0):
            raise ValueError("video_public_playback_unavailable")
        files = data.get("durl")
        if not isinstance(files, list) or len(files) != 1:
            raise ValueError("video_progressive_unavailable")
        duration, size = data.get("timelength"), files[0].get("size")
        if type(duration) is not int or not 0 < duration <= 2**63-1 or type(size) is not int or size <= 0:
            raise TypeError()
        locator = public_url(files[0]["url"])
        p = urlsplit(locator)
        if p.scheme != "https" or p.port is not None or not p.hostname.endswith(".bilivideo.com"):
            raise ValueError("video_public_playback_unavailable")
        return duration, [locator]
    except (KeyError, TypeError, AttributeError, json.JSONDecodeError):
        raise ValueError("video_page_metadata_unavailable") from None


def read_response(response, maximum):
    if not response.get("ok"):
        raise ValueError(response.get("error", "public_read_failed"))
    final = public_url(response["url"])
    body = base64.b64decode(response["body_base64"], validate=True)
    if len(body) > maximum:
        raise ValueError("host_response_exceeds_requested_bytes")
    return final, body


def photo_id(raw):
    p = urlsplit(public_url(raw))
    if p.hostname not in KUAI_HOSTS or p.port is not None:
        return ""
    match = PHOTO.fullmatch(p.path)
    if not match:
        return ""
    identity = match[1]
    ids = parse_qs(p.query, keep_blank_values=True).get("photoId", [])
    if any(value != identity for value in ids):
        raise ValueError("share_identity_conflict")
    return identity


def kuaishou_photo(body, identity):
    marker = STATE.search(body)
    if not marker:
        raise ValueError("video_page_metadata_unavailable")
    try:
        data, _ = json.JSONDecoder().raw_decode(body[marker.end():])
        client = data["defaultClient"]
        if not isinstance(client, dict) or len(client) > 10000:
            raise ValueError()
        photo = client["VisionVideoDetailPhoto:" + identity]
        if not isinstance(photo, dict) or photo.get("id") != identity:
            raise ValueError()
        duration, caption = photo.get("duration"), photo.get("caption", "")
        if type(duration) is not int or not 0 < duration <= 3600000 or not isinstance(caption, str) or len(caption.encode("utf8")) > 4096:
            raise ValueError()
    except (KeyError, TypeError, ValueError):
        raise ValueError("video_page_metadata_unavailable") from None

    def resolve(value):
        visited = set()
        for _ in range(8):
            if not isinstance(value, dict) or value.get("type") != "id":
                return value
            key = value.get("id")
            if not isinstance(key, str) or not key or key in visited:
                return None
            visited.add(key)
            value = client.get(key)
        return None

    urls = []

    def add(value):
        try:
            normalized = public_url(value)
            p = urlsplit(normalized)
            if p.scheme == "https" and p.port is None and p.hostname.endswith((".kwaicdn.com", ".djvod.ndcimgs.com")) and normalized not in urls:
                urls.append(normalized)
        except (TypeError, ValueError):
            pass

    add(photo.get("photoUrl"))
    manifest = resolve(photo.get("manifest"))
    sets = manifest.get("adaptationSet", []) if isinstance(manifest, dict) else []
    if isinstance(sets, list) and len(sets) <= 20:
        for item in sets:
            item = resolve(item)
            reps = item.get("representation", []) if isinstance(item, dict) else []
            if not isinstance(reps, list) or len(reps) > 30:
                continue
            for rep in reps:
                rep = resolve(rep)
                if not isinstance(rep, dict):
                    continue
                for key in ("url", "backupUrl"):
                    value = resolve(rep.get(key))
                    if isinstance(value, str):
                        add(value)
                    elif isinstance(value, dict) and isinstance(value.get("json"), list) and len(value["json"]) <= 10:
                        for url in value["json"]:
                            add(url)
    if not urls:
        raise ValueError("video_public_playback_unavailable")
    return caption, duration, urls


class ShareHTML(HTMLParser):
    def __init__(self, base):
        super().__init__(convert_charrefs=True)
        self.base, self.hidden, self.title_depth = base, 0, 0
        self.title, self.parts, self.media = [], [], []

    def handle_starttag(self, tag, attrs):
        if tag in ("script", "style", "template", "noscript"):
            self.hidden += 1
        if self.hidden:
            return
        if tag == "title":
            self.title_depth += 1
        if tag == "meta":
            values = dict(attrs)
            prop = (values.get("property") or values.get("name") or "").lower()
            if prop in ("og:video", "og:video:url", "og:video:secure_url"):
                try:
                    locator = public_url(urljoin(self.base, values.get("content") or ""))
                    if values.get("content") and locator not in self.media:
                        self.media.append(locator)
                except ValueError:
                    pass

    def handle_endtag(self, tag):
        if tag in ("script", "style", "template", "noscript"):
            self.hidden = max(0, self.hidden - 1)
        if tag == "title":
            self.title_depth = max(0, self.title_depth - 1)

    def handle_data(self, text):
        if not self.hidden:
            (self.title if self.title_depth else self.parts).append(text)


def resolve(args, get):
    allowed = {"url", "video_required", "offset", "limit", "expected_sha256", "max_bytes", "timeout_seconds", "candidate_offset", "candidate_limit"}
    if not isinstance(args, dict) or set(args) - allowed or type(args.get("video_required", False)) is not bool:
        raise ValueError("invalid_share_arguments")
    original = public_url(args.get("url"))
    maximum = integer(args, "max_bytes", 2 * 1024 * 1024, 1)
    if maximum > MAX_PAGE_BYTES:
        raise ValueError("page_transport_limit_exceeded")
    timeout = integer(args, "timeout_seconds", 30, 1)
    offset, limit = integer(args, "offset", 0, 0), integer(args, "limit", 14000, 1)
    candidate_offset = integer(args, "candidate_offset", 0, 0)
    candidate_limit = integer(args, "candidate_limit", 20, 1)
    original_id = photo_id(original)
    original_bili = bili_identity(original)
    original_host = urlsplit(original)
    if original_host.hostname == "b23.tv" and (original_host.port is not None or not BILI_SHORT.fullmatch(original_host.path)):
        raise ValueError("video_page_unsupported")
    response = get(original, maximum, timeout)
    final, body = read_response(response, maximum)
    identity = photo_id(final)
    final_bili = bili_identity(final)
    if original_id and identity != original_id:
        raise ValueError("share_identity_conflict")
    if original_bili and final_bili != original_bili or original_host.hostname == "b23.tv" and not final_bili:
        raise ValueError("share_identity_conflict")
    content_type = response.get("content_type", "").lower()
    kind = content_type.split(";", 1)[0].strip()
    title, text, locators, duration = "", "", [], None
    platform = "public"
    if final_bili:
        if not (kind.startswith("text/") or "html" in kind):
            raise ValueError("video_page_metadata_unavailable")
        cid, title = bili_metadata(body.decode("utf8", errors="replace"), final_bili)
        bvid, part = final_bili
        api = "https://api.bilibili.com/x/player/playurl?" + urlencode({"bvid": bvid, "cid": cid, "qn": 32, "fnval": 0, "fourk": 0})
        api_final, playback = read_response(get(api, maximum, timeout), maximum)
        if api_final != api:
            raise ValueError("share_identity_conflict")
        duration, locators = bili_playback(playback)
        identity, platform = bvid, "bilibili"
        final = "https://www.bilibili.com/video/" + bvid + "/" + ("?p=" + str(part) if part > 1 else "")
    elif kind.startswith("video/"):
        locators = [final]
    elif kind.startswith("image/") and not args.get("video_required"):
        locators = [final]
    elif kind.startswith("text/") or "html" in kind:
        encoding = "utf8"
        match = re.search(r'charset\s*=\s*["\']?([\w-]+)', content_type)
        if match:
            encoding = match[1]
        decoded = body.decode(encoding, errors="replace")
        if identity:
            platform = "kuaishou"
            canonical = "https://www.kuaishou.com/short-video/" + identity
            try:
                title, duration, locators = kuaishou_photo(decoded, identity)
            except ValueError:
                # As in the existing adapter, retry only the same exact work
                # on its canonical page. Preserve share parameters for reading,
                # never include them in the displayed source or search query.
                query = urlsplit(original).query
                retry = canonical + ("?" + query if query else "")
                if retry == original:
                    raise
                replacement = get(retry, maximum, timeout)
                if not replacement.get("ok"):
                    raise ValueError(replacement.get("error", "public_read_failed"))
                if photo_id(replacement["url"]) != identity:
                    raise ValueError("share_identity_conflict")
                content = base64.b64decode(replacement["body_base64"], validate=True)
                if len(content) > maximum:
                    raise ValueError("host_response_exceeds_requested_bytes")
                title, duration, locators = kuaishou_photo(content.decode("utf8", errors="replace"), identity)
            final = canonical
        else:
            host = urlsplit(final).hostname
            if host in KUAI_HOSTS or host in {"v.kuaishou.com", "weixin.qq.com", "channels.weixin.qq.com", "www.bilibili.com", "bilibili.com", "b23.tv"}:
                # These pages need a platform adapter. App landing text is never
                # a substitute for the received video's actual content.
                raise ValueError("share_platform_adapter_required")
            parser = ShareHTML(final)
            parser.feed(decoded)
            title = " ".join(" ".join(parser.title).split())
            text = " ".join(" ".join(parser.parts).split())
            locators = parser.media
            if locators or args.get("video_required"):
                text = ""
            if not locators and (args.get("video_required") or len(text) < 120):
                raise ValueError("share_requires_browser")
    else:
        raise ValueError("share_content_unsupported")
    source_url = final if platform == "bilibili" else display_url(final)
    digest = hashlib.sha256(json.dumps([source_url, title, text, locators, duration, identity], ensure_ascii=False, separators=(",", ":")).encode("utf8")).hexdigest()
    if args.get("expected_sha256") and args["expected_sha256"] != digest:
        raise ValueError("share_content_changed")
    if offset > len(text) or candidate_offset > len(locators):
        raise ValueError("offset_outside_share")
    end, candidate_end = min(len(text), offset + limit), min(len(locators), candidate_offset + candidate_limit)
    return {"ok": True, "platform": platform, "source_url": source_url, "title": title,
            "object_id": identity, "duration_ms": duration, "text": text[offset:end], "content_sha256": digest,
            "offset": offset, "requested_limit": limit, "total_characters": len(text), "next_offset": end if end < len(text) else None,
            "candidate_offset": candidate_offset, "requested_candidate_limit": candidate_limit, "total_candidates": len(locators),
            "next_candidate_offset": candidate_end if candidate_end < len(locators) else None,
            "private_media_locators": locators[candidate_offset:candidate_end],
            "coverage": "page_text" if text else "media_locator_only", "video_observed": False,
            "media_downloaded": False, "untrusted_external_content": True,
            "complete": end == len(text) and candidate_end == len(locators)}
