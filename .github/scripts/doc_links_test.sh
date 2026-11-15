#!/usr/bin/env bash
# Every relative link in the documentation points at a file that exists, and every #anchor at a heading that exists in it.
set -euo pipefail
cd "$(dirname "$0")/../.."
python3 - <<'PY'
import glob, os, re, sys

def slug(title):
    t = re.sub(r"[`*_]", "", title.strip().lower())
    t = re.sub(r"[^\w\- ]", "", t)
    return t.replace(" ", "-")

def anchors(path):
    out, fence = set(), False
    for line in open(path, encoding="utf8"):
        if line.startswith("```"):
            fence = not fence
        m = None if fence else re.match(r"^#{1,6} (.*)$", line)
        if m:
            out.add(slug(m.group(1)))
    return out

files = ["README.md", "CHANGELOG.md"] + sorted(glob.glob("docs/**/*.md", recursive=True))
cache, bad = {}, []
for f in files:
    text = open(f, encoding="utf8").read()
    text = re.sub(r"```.*?```", "", text, flags=re.S)
    for m in re.finditer(r"\]\(([^)\s]+)\)", text):
        url = m.group(1)
        if re.match(r"^[a-z]+:", url):
            continue
        path, _, frag = url.partition("#")
        target = f if path == "" else os.path.normpath(os.path.join(os.path.dirname(f), path))
        if not os.path.exists(target):
            bad.append(f"{f}: {url}: no such file")
            continue
        if frag and target.endswith(".md"):
            cache.setdefault(target, anchors(target))
            if frag not in cache[target]:
                bad.append(f"{f}: {url}: no such heading")
if bad:
    print("\n".join(bad))
    sys.exit(1)
print("every documentation link and anchor exists")
PY
