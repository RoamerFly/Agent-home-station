"""Video Channels protocol adapter. Credentials remain in host route proxies.

Resolves locators only; byte decryption, media validation and observation are
separate steps. This code never sends WeChat messages or invents a summary.
"""
import re
from urllib.parse import parse_qs, urlsplit, urlunsplit


def share_url(value):
    if not isinstance(value, str):
        raise ValueError("channels_share_link_invalid")
    p = urlsplit(value.strip())
    if p.scheme != "https" or p.netloc != "weixin.qq.com" or not re.fullmatch(r"/sph/[A-Za-z0-9_-]{6,128}", p.path):
        raise ValueError("channels_share_link_invalid")
    return urlunsplit((p.scheme, p.netloc, p.path, "", ""))


def uint64(value, code, required=False):
    if value is None and not required:
        return ""
    if type(value) is int:
        value = str(value)
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{1,20}", value) or int(value) > (1 << 64)-1:
        raise ValueError(code)
    return value


def resolve(args, route):
    if not isinstance(args, dict) or set(args)-{"url", "export_id", "nonce_id", "expected_object_id", "timeout_seconds"}:
        raise ValueError("invalid_channels_arguments")
    timeout = args.get("timeout_seconds", 25)
    if type(timeout) is not int or timeout < 1:
        raise ValueError("invalid_timeout_seconds")
    expected = uint64(args.get("expected_object_id"), "channels_object_id_invalid")
    has_url = bool(args.get("url"))
    if has_url and ("export_id" in args or "nonce_id" in args):
        raise ValueError("ambiguous_channels_source")
    if has_url:
        source = share_url(args["url"])
    else:
        export, nonce = args.get("export_id"), args.get("nonce_id")
        if not isinstance(export, str) or not export.startswith("export/") or not 12 <= len(export) <= 4096 or not isinstance(nonce, str) or not 1 <= len(nonce) <= 4096:
            raise ValueError("channels_assistant_export_id_required")
        result = route("channels-shortlink", {"exportId":export,"nonceId":nonce,"scene":40}, timeout)
        if type(result.get("errCode")) is not int or result["errCode"] != 0:
            raise ValueError("channels_assistant_shortlink_failed")
        source = share_url(result.get("data", {}).get("shortUrl"))
    parsed = route("channels-parse", {"type":"video_channel_url","url":source,"scene":1}, timeout)
    if type(parsed.get("code")) is not int or parsed["code"] != 0:
        raise ValueError("channels_yuanbao_parse_failed")
    data = parsed.get("data", {})
    play = urlsplit(data.get("playable_url", ""))
    if play.scheme != "https" or play.netloc != "channels.weixin.qq.com" or not play.path.startswith("/finder-preview/") or play.fragment:
        raise ValueError("channels_playable_link_missing")
    query = parse_qs(play.query, keep_blank_values=True)
    tokens, ids = query.get("token", []), query.get("eid", [])
    if len(tokens) != 1 or not 1 <= len(tokens[0]) <= 8192 or len(ids) > 1:
        raise ValueError("channels_playable_link_missing")
    identity = ids[0] if ids else data.get("wx_export_id")
    if not isinstance(identity, str) or not 1 <= len(identity) <= 4096:
        raise ValueError("channels_playable_link_missing")
    feed = route("channels-feed", {"baseReq":{"generalToken":tokens[0]},"exportId":identity}, timeout, play.geturl())
    if type(feed.get("errCode")) is not int or feed["errCode"] != 0:
        raise ValueError("channels_feed_lookup_failed")
    feed_data = feed.get("data", {})
    info = feed_data.get("feedInfo", {})
    locator = (info.get("h264VideoInfo") or {}).get("videoUrl") or info.get("videoUrl", "")
    media = urlsplit(locator)
    if media.scheme != "https" or not media.hostname or media.username is not None or media.password is not None or len(locator) > 16384 or "\\" in locator or any(ord(c) < 33 for c in locator):
        raise ValueError("channels_media_locator_missing")
    actual = uint64(info.get("objectId"), "channels_resolver_response_invalid", bool(expected))
    if expected and actual != expected:
        raise ValueError("channels_resolved_video_mismatch")
    key = uint64(info.get("decodeKey"), "channels_decode_key_invalid")
    title, author = info.get("description", ""), feed_data.get("authorInfo", {}).get("nickname", "")
    if not isinstance(title, str) or not isinstance(author, str):
        raise ValueError("channels_resolver_response_invalid")
    return {"ok":True,"platform":"channels","source_url":source,"object_id":actual,"title":title,"author":author,
            "private_media_locators":[locator],"private_decode_key":key,"coverage":"media_locator_only",
            "video_observed":False,"media_downloaded":False,"untrusted_external_content":True}
