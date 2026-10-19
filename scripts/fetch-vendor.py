#!/usr/bin/env python3
"""Restores web/vendor's generated files (the terminal font) from the pinned
sources in web/vendor/manifest.json, checking each archive and file against
the recorded hashes. The repository keeps only the manifest and the license."""
import hashlib, io, json, os, sys, urllib.request, zipfile

root = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "web", "vendor")
manifest = json.load(open(os.path.join(root, "manifest.json")))
assets = manifest["assets"]

fonts = {
    "Moralerspace HW": {
        "MoralerspaceHW_v2.0.0/MoralerspaceNeonHW-Regular.ttf": "fonts/moralerspace-neon-hw-2.0.0-regular.ttf",
        "MoralerspaceHW_v2.0.0/MoralerspaceNeonHW-Bold.ttf": "fonts/moralerspace-neon-hw-2.0.0-bold.ttf",
    },
}


def fetch(url):
    with urllib.request.urlopen(url, timeout=120) as r:
        return r.read()


def write(name, data):
    if hashlib.sha256(data).hexdigest() != assets[name]:
        sys.exit("SHA-256 mismatch: " + name)
    path = os.path.join(root, name)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)


for record in manifest["fonts"]:
    data = fetch(record["archive"])
    if hashlib.sha256(data).hexdigest() != record["sha256"]:
        sys.exit("font archive SHA-256 mismatch: " + record["name"])
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        for member, name in fonts[record["name"]].items():
            write(name, z.read(member))
print("restored web/vendor")
