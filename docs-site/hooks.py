# SPDX-License-Identifier: Apache-2.0
"""MkDocs hooks of the documentation site (SPEC §17.4).

- Only the pages at the top of ``docs/``, the operations manual in ``docs/manual/`` and the
  files in ``docs/images/`` are part of the site; other subdirectories of ``docs/`` are left
  out. (``exclude_docs`` cannot express this: its patterns also apply to the theme's own files.)
- Each page's meta description is its summary paragraph: the first paragraph after the H1
  (SPEC §19.3), with Markdown markup removed, unless the page sets ``description`` itself.
- Relative links are resolved against the page. A link that stays inside ``docs/``
  (``../installation.md`` from the manual) is left to MkDocs; a link that leaves ``docs/``
  (``../SECURITY.md``, ``../../deploy/ha/README.md``) works on GitHub as it is and on the site
  points to the file in the repository on GitHub instead.
- The logo and favicon are the web UI's favicon (``web/public/favicon.svg``), copied into the
  built site, so there is only one copy of the mark in the repository. The offline bundle
  (``mkdocs.offline.yml``) also gets the vendored iframe-worker shim, which makes search work
  from ``file://`` without a CDN request.
- ``llms.txt`` (an index: title, summary and link of every page, grouped as the navigation) and
  ``llms-full.txt`` (the Markdown of every page in navigation order, HTML comments removed and
  relative links made absolute) are written to the site root for agents and language models.
"""

import posixpath
import re
import shutil
from pathlib import Path

REPO_BLOB = "https://github.com/ZeraOrg/opentidemark/blob/main/"
SITE_SUBDIRS = ("images/", "manual/")  # subdirectories of docs/ that belong to the site
LOGO_SOURCE = Path("web/public/favicon.svg")
LOGO_TARGET = Path("assets/logo.svg")  # theme.logo and theme.favicon in mkdocs.yml
SHIM_SOURCE = Path("docs-site/offline/iframe-worker.js")  # offline bundle only (mkdocs.offline.yml)
SHIM_TARGET = Path("assets/javascripts/iframe-worker.js")  # extra.polyfills in mkdocs.offline.yml
LLMS_INDEX = "llms.txt"
LLMS_FULL = "llms-full.txt"
TOP_LEVEL_SECTION = "Documentation"  # llms.txt heading for pages outside a navigation section

_RELATIVE_LINK = re.compile(r"\]\((?![a-z][a-z0-9+.-]*:|/|#)([^)\s#]+)(#[^)\s]*)?\)")
_COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
_LINK = re.compile(r"\[([^\]]*)\]\([^)]*\)")
_MARKUP = re.compile(r"`|\*\*|__")  # code spans and strong emphasis
_H1 = re.compile(r"(?m)^# (.+)$")

_nav_pages = {}  # config file path ⇒ the site's pages in navigation order (set by on_nav)


def summary(markdown: str) -> str:
    """Return the first paragraph after the H1 as plain text ("" when there is none)."""
    lines = _COMMENT.sub("", markdown).splitlines()
    start = next((i for i, line in enumerate(lines) if line.startswith("# ")), None)
    if start is None:
        return ""
    paragraph = []
    for line in lines[start + 1 :]:
        if line.strip():
            paragraph.append(line.strip())
        elif paragraph:
            break
    text = _MARKUP.sub("", _LINK.sub(r"\1", " ".join(paragraph)))
    return re.sub(r"\s+", " ", text).strip()


def _resolve(page_src: str, target: str) -> str:
    """Resolve a relative link target against the page's directory, as a path under docs/."""
    return posixpath.normpath(posixpath.join(posixpath.dirname(page_src), target))


def _rewrite_outside_links(markdown: str, page_src: str) -> str:
    """Point links that leave docs/ at the file on GitHub; leave the others to MkDocs."""

    def repl(match: re.Match) -> str:
        target, anchor = match.group(1), match.group(2) or ""
        resolved = _resolve(page_src, target)
        if not resolved.startswith("../"):
            return match.group(0)
        repo_path = posixpath.normpath(posixpath.join("docs", resolved))
        suffix = "/" if target.endswith("/") else ""
        return f"]({REPO_BLOB}{repo_path}{suffix}{anchor})"

    return _RELATIVE_LINK.sub(repl, markdown)


def on_files(files, config):
    docs_dir = Path(config["docs_dir"]).resolve()
    for file in list(files):
        from_docs = file.abs_src_path and Path(file.abs_src_path).resolve().is_relative_to(docs_dir)
        if from_docs and "/" in file.src_uri and not file.src_uri.startswith(SITE_SUBDIRS):
            files.remove(file)
    return files


def on_nav(nav, config, files):
    _nav_pages[config["config_file_path"]] = [
        (_section_title(page), page) for page in nav.pages
    ]
    return nav


def _section_title(page) -> str:
    """Return the title of the page's top-level navigation section."""
    item = page
    while item.parent is not None:
        item = item.parent
    return item.title if item.is_section else TOP_LEVEL_SECTION


def on_page_markdown(markdown, page, config, files):
    if not page.meta.get("description"):
        page.meta["description"] = summary(markdown) or config.get("site_description")
    return _rewrite_outside_links(markdown, page.file.src_uri)


def on_post_build(config):
    root = Path(config["config_file_path"]).parent
    site = Path(config["site_dir"])
    copies = [(LOGO_SOURCE, LOGO_TARGET)] + ([(SHIM_SOURCE, SHIM_TARGET)] if _offline(config) else [])
    for source, target in copies:
        (site / target).parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(root / source, site / target)
    pages = _nav_pages.get(config["config_file_path"], [])
    (site / LLMS_INDEX).write_text(_llms_index(pages, config), encoding="utf-8")
    (site / LLMS_FULL).write_text(_llms_full(pages, config), encoding="utf-8")


def _offline(config) -> bool:
    """True when Material's offline plugin builds the bundle (plugin key "material/offline")."""
    return any(name.endswith("offline") for name in config["plugins"])


def _page_url(page, config) -> str:
    """The page's address: absolute on the online site, relative in the offline bundle."""
    url = "" if page.file.url == "./" else page.file.url  # the home page of a directory-URL site
    if _offline(config):
        return url
    return config["site_url"] + url


def _page_markdown(page) -> str:
    return _COMMENT.sub("", Path(page.file.abs_src_path).read_text(encoding="utf-8")).strip()


def _llms_index(pages, config) -> str:
    out = [f"# {config['site_name']}", "", f"> {_one_line(config['site_description'])}", ""]
    current = None
    for section, page in pages:
        if section != current:
            out += [f"## {section}", ""]
            current = section
        markdown = _page_markdown(page)
        title = _H1.search(markdown)
        out.append(f"- [{title.group(1) if title else page.title}]({_page_url(page, config)}): {summary(markdown)}")
    out.append("")
    return "\n".join(out)


def _llms_full(pages, config) -> str:
    out = []
    for _, page in pages:
        markdown = _absolute_links(_page_markdown(page), page, config)
        url = _page_url(page, config)
        out.append(_H1.sub(lambda m: f"{m.group(0)}\n\nSource: {url}", markdown, count=1))
    return "\n\n---\n\n".join(out) + "\n"


def _absolute_links(markdown: str, page, config) -> str:
    """Rewrite the page's relative links to the addresses of the built site."""

    def repl(match: re.Match) -> str:
        target, anchor = match.group(1), match.group(2) or ""
        resolved = _resolve(page.file.src_uri, target)
        if resolved.startswith("../"):
            return match.group(0)  # already rewritten to GitHub by on_page_markdown
        if resolved.endswith(".md"):
            stem = resolved[: -len(".md")]
            if stem.endswith("index") or stem == "index":
                stem = posixpath.dirname(stem)
            if config["use_directory_urls"]:
                resolved = f"{stem}/" if stem else ""
            else:
                resolved = f"{stem}.html" if stem else "index.html"
        base = "" if _offline(config) else config["site_url"]
        return f"]({base}{resolved}{anchor})"

    return _RELATIVE_LINK.sub(repl, _rewrite_outside_links(markdown, page.file.src_uri))


def _one_line(text: str) -> str:
    return re.sub(r"\s+", " ", text or "").strip()
