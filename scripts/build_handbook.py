"""Build the public handbook. Output goes into a separate checkout or staging directory."""
import argparse
from html import escape
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1] / "handbook"
CHAPTERS = ("index", "installation", "first-chat", "conversations", "artifacts", "plugins", "operations")
WIKI = "https://github.com/RoamerFly/Agent-home-station/wiki/"
REPO = "https://github.com/RoamerFly/Agent-home-station"
STYLE = """
:root{color-scheme:light dark;font:17px/1.75 system-ui,sans-serif}
body{max-width:72rem;margin:auto;padding:1.5rem;color:CanvasText;background:Canvas}
header{border-bottom:1px solid GrayText;padding-bottom:.7rem}main{display:grid;grid-template-columns:12rem minmax(0,1fr);gap:2rem}
nav{margin-top:1.4rem}nav a{display:block;padding:.3rem}article{max-width:54rem;overflow-wrap:anywhere}
a{color:LinkText}a[aria-current]{font-weight:bold}pre,table{display:block;overflow-x:auto}
pre{padding:1rem;background:color-mix(in srgb,CanvasText 7%,Canvas)}th,td{padding:.4rem;text-align:left;border-bottom:1px solid GrayText}
@media(max-width:720px){body{padding:1rem}main{display:block}nav a{display:inline-block;margin-right:.5rem}}
"""


def page_link(slug, target):
    if target == "wiki":
        return WIKI + ("Home" if slug == "index" else slug)
    return slug + ".html"


def build(target, output):
    chapters = {}
    for slug in CHAPTERS:
        body = (ROOT / (slug + ".md")).read_text(encoding="utf-8-sig")
        title, _ = body.split("\n", 1)
        if not title.startswith("# "):
            raise ValueError(f"Missing chapter title: {slug}")
        chapters[slug] = (title[2:], body)

    def link(match):
        name = match[1]
        if name not in chapters:
            raise ValueError(f"Unknown handbook chapter: {name}")
        return "](" + page_link(name, target) + ")"

    output.mkdir(parents=True, exist_ok=True)
    for slug, (title, body) in chapters.items():
        body = re.sub(r"\]\(([\w-]+)\.md\)", link, body)
        if target == "wiki":
            name = "Home" if slug == "index" else slug
            (output / (name + ".md")).write_text(body, encoding="utf-8")
            continue
        import markdown
        content = markdown.markdown(body, extensions=["tables", "fenced_code", "sane_lists", "toc"])
        nav = "".join(f'<a href="{page_link(other, target)}"'
                      + (' aria-current="page"' if other == slug else '')
                      + f'>{escape(data[0])}</a>' for other, data in chapters.items())
        html = (f'<!doctype html><html lang="zh-CN"><meta charset="utf-8">'
                f'<meta name="viewport" content="width=device-width,initial-scale=1">'
                f'<title>{escape(title)} · Agent-home-station</title><style>{STYLE}</style>'
                f'<header>Agent-home-station　<a href="{REPO}">GitHub</a></header>'
                f'<main><nav aria-label="手册目录">{nav}</nav><article>{content}</article></main></html>')
        (output / (slug + ".html")).write_text(html, encoding="utf-8")
    if target == "wiki":
        sidebar = "\n".join(f"- [{data[0]}]({page_link(slug, target)})" for slug, data in chapters.items())
        (output / "_Sidebar.md").write_text(sidebar + "\n", encoding="utf-8")
    else:
        (output / ".nojekyll").touch()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("target", choices=["pages", "wiki"])
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    build(args.target, args.output)
