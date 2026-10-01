#!/usr/bin/env python3
"""Helpers for the release workflow (also usable locally).

  release_tools.py version              print VERSION from app.py
  release_tools.py notes X.Y.Z          print that version's CHANGELOG.md section
  release_tools.py package X.Y.Z DIR    build DIR/pbswebclient-X.Y.Z.tar.gz and .sha256
"""
import hashlib
import os
import re
import sys
import tarfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PACKAGE_FILES = ["app.py", "qr.py", "static", "install.sh", "uninstall.sh",
                 "README.md", "LICENSE", "CHANGELOG.md", "docs"]


def version():
    with open(os.path.join(ROOT, "app.py")) as f:
        match = re.search(r'^VERSION = "([^"]+)"', f.read(), re.M)
    if not match:
        sys.exit("VERSION not found in app.py")
    return match.group(1)


def notes(ver):
    with open(os.path.join(ROOT, "CHANGELOG.md")) as f:
        text = f.read()
    match = re.search(rf"^## \[{re.escape(ver)}\][^\n]*\n(.*?)(?=^## |^\[[^\]]+\]: |\Z)", text, re.M | re.S)
    if not match:
        sys.exit(f"No CHANGELOG.md section for {ver}")
    body = match.group(1).strip()
    return (body + "\n\n**Install or upgrade:** download `pbswebclient-" + ver + ".tar.gz` below, then\n"
            "```bash\ntar xzf pbswebclient-" + ver + ".tar.gz && cd pbswebclient-" + ver + " && sudo ./install.sh\n```\n")


def package(ver, out_dir):
    os.makedirs(out_dir, exist_ok=True)
    name = f"pbswebclient-{ver}"
    path = os.path.join(out_dir, f"{name}.tar.gz")

    def clean(info):
        if "__pycache__" in info.name or info.name.endswith(".pyc"):
            return None
        info.uid = info.gid = 0
        info.uname = info.gname = "root"
        return info

    with tarfile.open(path, "w:gz") as tar:
        for item in PACKAGE_FILES:
            tar.add(os.path.join(ROOT, item), arcname=f"{name}/{item}", filter=clean)
    with open(path, "rb") as f:
        digest = hashlib.sha256(f.read()).hexdigest()
    with open(path + ".sha256", "w") as f:
        f.write(f"{digest}  {name}.tar.gz\n")
    print(path)


if __name__ == "__main__":
    if len(sys.argv) >= 2 and sys.argv[1] == "version":
        print(version())
    elif len(sys.argv) == 3 and sys.argv[1] == "notes":
        sys.stdout.write(notes(sys.argv[2]))
    elif len(sys.argv) == 4 and sys.argv[1] == "package":
        package(sys.argv[2], sys.argv[3])
    else:
        sys.exit(__doc__)
