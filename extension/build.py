#!/usr/bin/env python3
"""Builds the BDDM extension from this one source folder.

  python build.py            (both)
  python build.py firefox    (only Firefox)

Makes:
  dist/chromium/  and ../BDDM-chromium-extension.zip          (Chrome, Edge, Brave)
  dist/firefox/   and ../BDDM-firefox-extension.zip + ../BDDM-firefox.xpi
The only difference between the builds is manifest.json.
"""
import json, os, shutil, zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.dirname(HERE)
FILES = ["background.js", "hls_capture.js", "overlay_rules.js", "overlay.js", "videos.js", "compat.js", "firefox_capture.js", "handback.js", "content.js", "popup.html", "popup.css", "popup.js", "dup.html", "dup.js", "savepdf.js", "savepdf-bg.js"]


def merge(a, b):
    # Append permission lists instead of replacing them, so firefox.json can
    # add "webRequestBlocking" without copying the whole permissions array.
    LIST_KEYS = ("permissions", "host_permissions", "optional_permissions")
    for k, v in b.items():
        if k in LIST_KEYS and isinstance(v, list) and isinstance(a.get(k), list):
            seen = set(a[k])
            for item in v:
                if item not in seen:
                    a[k].append(item)
                    seen.add(item)
        elif isinstance(v, dict) and isinstance(a.get(k), dict):
            a[k] = merge(a.get(k, {}), v)
        else:
            a[k] = v
    return a


def build(target):
    base = json.load(open(os.path.join(HERE, "manifests", "base.json")))
    extra = json.load(open(os.path.join(HERE, "manifests", target + ".json")))
    d = os.path.join(HERE, "dist", target)
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(os.path.join(d, "icons"))
    for f in FILES:
        shutil.copy(os.path.join(HERE, f), d)
    for f in os.listdir(os.path.join(HERE, "icons")):
        shutil.copy(os.path.join(HERE, "icons", f), os.path.join(d, "icons"))
    with open(os.path.join(d, "manifest.json"), "w") as fh:
        json.dump(merge(base, extra), fh, indent=2)
    return d


def zipdir(d, out, prefix):
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for root, _, files in os.walk(d):
            for fn in sorted(files):
                p = os.path.join(root, fn)
                z.write(p, prefix + os.path.relpath(p, d).replace(os.sep, "/"))


if __name__ == "__main__":
    import sys
    only = sys.argv[1] if len(sys.argv) > 1 else ""  # "firefox" or "chromium"
    c = None
    if only != "firefox":
        c = build("chromium")
        zipdir(c, os.path.join(OUT, "BDDM-chromium-extension.zip"), "BDDM-extension/")
    if only == "chromium":
        print("built", c)
        sys.exit(0)
    f = build("firefox")
    zipdir(f, os.path.join(OUT, "BDDM-firefox-extension.zip"), "BDDM-firefox-extension/")
    zipdir(f, os.path.join(OUT, "BDDM-firefox.xpi"), "")  # .xpi: manifest.json at the top
    print("built", " and ".join(x for x in (c, f) if x))
