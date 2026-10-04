"""Compare locked release inputs with official stable upstream metadata."""
import json
import re
import subprocess
from urllib.request import urlopen

from release_config import ROOT, CONFIG


def document(url):
    with urlopen(url,timeout=30) as response:
        return json.load(response)


def github(path):
    return json.loads(subprocess.check_output(["gh","api",path],text=True,timeout=45))


def verify():
    go = next(item["version"] for item in document("https://go.dev/dl/?mode=json") if item["stable"])
    if go != "go"+CONFIG["go"]:
        raise ValueError("Go stable pin requires a reviewed source update")
    node = document("https://nodejs.org/dist/index.json")[0]["version"].removeprefix("v")
    chrome = document("https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json")["channels"]["Stable"]["version"]
    for path in (ROOT/"packaging/tools/inputs").glob("browser-*-*.json"):
        recipe=json.loads(path.read_text())
        if "assets" in recipe and (recipe["assets"]["node"]["version"] != node or recipe["assets"]["chrome"]["version"] != chrome):
            raise ValueError("browser stable Node/Chrome receipts require a reviewed update: "+path.name)
    package=json.loads((ROOT/"modules/browser/package.json").read_text())
    for name in ("@playwright/mcp","chrome-devtools-mcp"):
        expected=document("https://registry.npmjs.org/"+name.replace("/","%2f"))["dist-tags"]["latest"]
        if package["dependencies"][name] != expected:
            raise ValueError("browser MCP stable pin requires a reviewed update: "+name)
    workflow=(ROOT/".github/workflows/release.yml").read_text()
    for repository, locked in sorted(set(re.findall(r"uses: ([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([a-f0-9]{40})",workflow))):
        release=github(f"repos/{repository}/releases/latest")
        ref=github(f"repos/{repository}/git/ref/tags/{release['tag_name']}")["object"]
        if ref["type"]=="tag":ref=github(f"repos/{repository}/git/tags/{ref['sha']}")["object"]
        if ref["sha"] != locked:
            raise ValueError("Action stable pin requires a reviewed update: "+repository)
    from build_go_vendor import OWNERS
    for owner,repository in (("github","cli/cli"),("coordination","jinyongp/devtools")):
        if github(f"repos/{repository}/releases/latest")["tag_name"] != "v"+OWNERS[owner]["version"]:
            raise ValueError("native stable tool pin requires a reviewed update: "+repository)
    for repository,selected in (("moby/buildkit","v0.33.1"),("docker/buildx","v0.37.2")):
        if github(f"repos/{repository}/releases/latest")["tag_name"] != selected:
            raise ValueError("image builder stable pin requires a reviewed update: "+repository)
    print("Official stable tool and action pins verified.",flush=True)


if __name__=="__main__":verify()
