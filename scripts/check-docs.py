#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
"""Check that the documentation still describes the code.

Prose cannot be compiled, so a doc that drifts from the source stays wrong
until somebody happens to read both. That is not hypothetical: every one of
these checks exists because it caught something real.

  commands  The docs named `scour train`, `scour stream`, `scour mark`,
            `scour rules`, `scour import` and `scour join` for a while after
            those became verbs under a noun. Six commands that errored.
  config    The schedule page documented `[crawl] refresh`, a key
            internal/config has never had.
  registry  Guards the implementation names in the extension-points table
            against a registry entry being renamed or removed.
  shorthands  The --type table against internal/content. `feed` was missing
            from it, which is a whole content type a reader could not know to
            ask for.
  routes    The HTTP table against the routes the server registers.
  links     Internal links, images and heading anchors, which rot silently
            when a page is renamed.
  diagrams  Every SVG parses, and none is orphaned.
  pager     The prev/next chain matches the sidebar order, which is easy to
            break when a page is inserted.

Run it with `make docs-check`. It needs no third-party packages. The command
check needs a built binary and says so rather than passing quietly when there
is none.
"""

from __future__ import annotations

import glob
import os
import re
import subprocess
import sys
import xml.dom.minidom

DOCS = "docs"
# Design documents describe a surface that is deliberately ahead of the code,
# so their command forms are not checked against the binary.
DESIGN = ("cli/design.md", "server/api.md", "plan/index.md")

NOUNS = {"item", "job", "record", "model", "node"}
SHORTCUTS = {"run", "crawl", "start", "status", "top", "server", "mcp", "version", "help"}

failures: list[str] = []
notes: list[str] = []


def fail(check: str, detail: str) -> None:
    failures.append(f"{check}: {detail}")


def pages() -> list[str]:
    return sorted(glob.glob(f"{DOCS}/**/*.md", recursive=True))


def prose_pages() -> list[str]:
    return [p for p in pages() if not p.endswith(DESIGN)] + ["README.md"]


def page_url(path: str) -> str:
    rel = path[len(DOCS) + 1 :]
    if rel == "index.md":
        return "/"
    if rel.endswith("/index.md"):
        return "/" + rel[: -len("index.md")]
    return "/" + rel[:-3] + ".html"


def target_file(link: str) -> str:
    if link == "/":
        return f"{DOCS}/index.md"
    if link.endswith("/"):
        return f"{DOCS}/{link.strip('/')}/index.md"
    if link.endswith(".html"):
        return f"{DOCS}/{link.lstrip('/')[:-5]}.md"
    return f"{DOCS}/{link.lstrip('/')}"


def heading_ids(path: str) -> set[str]:
    """The ids kramdown will generate, including explicit {: #id } overrides."""
    lines = open(path, encoding="utf-8").read().split("\n")
    ids: set[str] = set()
    for i, line in enumerate(lines):
        m = re.match(r"^#{2,4}\s+(.*)", line)
        if not m:
            continue
        ial = re.match(r"^\{:\s*#([\w-]+)\s*\}", lines[i + 1].strip()) if i + 1 < len(lines) else None
        if ial:
            ids.add(ial.group(1))
        else:
            text = re.sub(r"[`*\[\](){},.?:;\"']", "", m.group(1).lower())
            ids.add(re.sub(r"[^a-z0-9]+", "-", text).strip("-"))
    return ids


def check_commands(binary: str | None) -> None:
    if not binary:
        notes.append("commands: SKIPPED, no binary (run `make build` first)")
        return
    forms: set[tuple[str, ...]] = set()
    for path in prose_pages():
        src = open(path, encoding="utf-8").read()
        for m in re.finditer(r"\bscour (?:--\w+ )?([a-z][a-z-]*)(?: ([a-z][a-z-]*))?", src):
            noun, verb = m.group(1), m.group(2)
            if noun in NOUNS and verb:
                forms.add((noun, verb))
            elif noun in SHORTCUTS:
                forms.add((noun,))
    for form in sorted(forms):
        r = subprocess.run([binary, *form, "--help"], capture_output=True, text=True)
        if r.returncode != 0:
            fail("commands", f"`scour {' '.join(form)}` is documented and the binary rejects it")
    notes.append(f"commands: {len(forms)} forms checked against the binary")


def check_config_keys() -> None:
    tags: set[str] = set()
    for path in glob.glob("internal/config/*.go"):
        tags |= set(re.findall(r'toml:"([a-z_]+)"', open(path, encoding="utf-8").read()))
    # [cache.options] is a map[string]string, so its keys are the driver's, not ours.
    freeform = {"region"}
    checked = 0
    for path in prose_pages():
        src = open(path, encoding="utf-8").read()
        for block in re.findall(r"```toml\n(.*?)```", src, re.S):
            in_options = False
            for line in block.split("\n"):
                if re.match(r"\s*#?\s*\[cache\.options\]", line):
                    in_options = True
                    continue
                if re.match(r"\s*#?\s*\[", line):
                    in_options = False
                m = re.match(r"\s*#?\s*([a-z_]+)\s*=", line)
                if not m or in_options:
                    continue
                checked += 1
                key = m.group(1)
                if key not in tags and key not in freeform:
                    fail("config", f"`{key}` is documented in {path} and is not a toml tag in internal/config")
    notes.append(f"config: {checked} key mentions checked against {len(tags)} real tags")


def check_registry_names() -> None:
    registered: set[str] = {"local", "s3", "gcs"}  # cache drivers register via constants
    for path in glob.glob("internal/**/*.go", recursive=True):
        if path.endswith("_test.go"):
            continue
        registered |= set(
            re.findall(r'\bRegister(?:Refresh)?\("([a-z0-9]+)"', open(path, encoding="utf-8").read())
        )
    table = open(f"{DOCS}/architecture/extending.md", encoding="utf-8").read()
    section = table[table.index("## The extension points") :]
    section = section[: section.index("##", 5)]
    checked = 0
    for row in re.findall(r"^\|.*\|$", section, re.M):
        if "---" in row:
            continue
        # Liquid carries its own pipes, so they have to go before the row is
        # split into cells. Without this the split silently yields the wrong
        # column and the check passes while testing nothing.
        cells = re.sub(r"\{\{.*?\}\}", "", row).strip("|").split("|")
        if len(cells) != 4:
            fail("registry", f"cannot read the extension table row: {row.strip()}")
            continue
        for name in re.findall(r"`([a-z0-9]+)`", cells[2]):
            checked += 1
            if name not in registered:
                fail("registry", f"the extension table offers `{name}` and nothing registers it")
    if not checked:
        fail("registry", "no implementation names were found: the table shape changed")
    notes.append(f"registry: {checked} implementation names checked against {len(registered)} registered")


def check_shorthands() -> None:
    """The --type shorthand table against the map it documents.

    `feed` was missing from this table for a while, which is a whole content
    type a reader would not know they could ask for.
    """
    source = ""
    for path in glob.glob("internal/content/*.go"):
        text = open(path, encoding="utf-8").read()
        if "Shorthands = map" in text:
            source = text
    block = re.search(r"Shorthands = map\[string\]\[\]string\{(.*?)\n\}", source, re.S)
    if not block:
        fail("shorthands", "cannot find the Shorthands map in internal/content")
        return
    real = {
        m.group(1): set(re.findall(r'"([^"]+)"', m.group(2)))
        for m in re.finditer(r'"([a-z]+)":\s*\{(.*?)\}', block.group(1), re.S)
    }
    doc = open(f"{DOCS}/crawl/index.md", encoding="utf-8").read()
    table = doc[doc.index("| Shorthand |") :]
    table = table[: table.index("\n\n")]
    documented = {}
    for row in table.split("\n")[2:]:
        cells = [c.strip() for c in row.strip("|").split("|")]
        if len(cells) == 2:
            documented[cells[0].strip("`")] = set(re.findall(r"`([^`]+)`", cells[1]))
    for name in sorted(set(real) - set(documented)):
        fail("shorthands", f"`{name}` is a --type shorthand and the crawl page does not list it")
    for name in sorted(set(documented) - set(real)):
        fail("shorthands", f"the crawl page lists `{name}` and internal/content has no such shorthand")
    for name in sorted(set(real) & set(documented)):
        if real[name] != documented[name]:
            fail("shorthands", f"`{name}` expands to {sorted(real[name])}, documented as {sorted(documented[name])}")
    notes.append(f"shorthands: {len(real)} content-type shorthands checked")


def check_routes() -> None:
    """The HTTP route table against the routes the server registers."""
    server = open("internal/server/server.go", encoding="utf-8").read()
    real = set(re.findall(r'mux\.HandleFunc\("\w+ ([^"+]+)"', server))
    real |= set(re.findall(r'mux\.Handle\("([^"]+)"', server))
    real = {p.rstrip("/") or "/" for p in real}
    doc = open(f"{DOCS}/server/index.md", encoding="utf-8").read()
    table = doc[doc.index("| Method | Path | Does |") :]
    table = table[: table.index("\n\n")]
    documented = {m.rstrip("/") or "/" for m in re.findall(r"`(/[^`]*)`", table)}
    # The metrics path is configurable, so it is not a literal in the source.
    for path in sorted(real - documented - {"/metrics"}):
        fail("routes", f"the server serves {path} and the server page does not list it")
    for path in sorted(documented - real - {"/metrics"}):
        fail("routes", f"the server page lists {path} and the server serves no such route")
    notes.append(f"routes: {len(real)} HTTP routes checked")


def check_links() -> None:
    sources = pages() + [f"{DOCS}/_layouts/default.html"]
    used_images: set[str] = set()
    links = anchors = 0
    for path in sources:
        src = open(path, encoding="utf-8").read()
        for m in re.finditer(r"\{\{\s*'([^']+)'\s*\|\s*relative_url\s*\}\}(#[\w-]+)?", src):
            link, frag = m.group(1), m.group(2)
            if link.startswith("/img/"):
                used_images.add(f"{DOCS}{link}")
                if not os.path.exists(f"{DOCS}{link}"):
                    fail("links", f"{path} references missing image {link}")
                continue
            links += 1
            target = target_file(link)
            if not os.path.exists(target):
                fail("links", f"{path} links to {link}, which is no page")
            elif frag:
                anchors += 1
                if frag[1:] not in heading_ids(target):
                    fail("links", f"{path} links to {link}{frag}, and that heading does not exist")
    for svg in sorted(glob.glob(f"{DOCS}/img/*.svg")):
        if svg not in used_images:
            fail("diagrams", f"{svg} is not shown on any page")
    notes.append(f"links: {links} internal links and {anchors} anchors resolved")


def check_diagrams() -> None:
    svgs = sorted(glob.glob(f"{DOCS}/img/*.svg"))
    for svg in svgs:
        try:
            xml.dom.minidom.parse(svg)
        except Exception as exc:  # noqa: BLE001 - the message is the useful part
            fail("diagrams", f"{svg} is not well-formed XML: {exc}")
    notes.append(f"diagrams: {len(svgs)} SVGs parsed")


def check_pager() -> None:
    layout = open(f"{DOCS}/_layouts/default.html", encoding="utf-8").read()
    nav = re.findall(r"<li><a href=\"\{\{ '([^']+)' \| relative_url \}\}\"", layout)
    pagers: dict[str, list[str] | None] = {}
    for path in pages():
        block = re.search(r'<div class="pager".*?</div>', open(path, encoding="utf-8").read(), re.S)
        pagers[page_url(path)] = (
            re.findall(r"\{\{ '([^']+)' \| relative_url \}\}", block.group(0)) if block else None
        )
    for i, url in enumerate(nav):
        if url == "/":  # the landing page is a hub, not a step in the sequence
            continue
        expected = [u for u in (nav[i - 1] if i else None, nav[i + 1] if i + 1 < len(nav) else None) if u]
        if pagers.get(url) != expected:
            fail("pager", f"{url} has prev/next {pagers.get(url)}, and the sidebar order wants {expected}")
    notes.append(f"pager: {len(nav)} pages checked against the sidebar order")


def main() -> int:
    if not os.path.isdir(DOCS):
        print("run this from the repository root", file=sys.stderr)
        return 2
    binary = next((p for p in ("bin/scour", "./scour") if os.path.exists(p)), None)

    check_commands(binary)
    check_config_keys()
    check_registry_names()
    check_shorthands()
    check_routes()
    check_links()
    check_diagrams()
    check_pager()

    for note in notes:
        print(f"  {note}")
    if failures:
        print(f"\n{len(failures)} problem(s):", file=sys.stderr)
        for f in failures:
            print(f"  {f}", file=sys.stderr)
        return 1
    print("\ndocs check passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
