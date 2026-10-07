"""One workflow's selective native builds, immutable reuse and release assembly."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import tempfile
from urllib.request import urlopen
import zipfile

from plan_release import TARGETS, ROLES, OWNERS, plan as make_plan
from release_config import ROOT, CONTRACT, CONFIG, version

SCRIPTS = Path(__file__).resolve().parent
SHA = re.compile(r"[a-f0-9]{64}")


def read(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 8 << 20:
        raise ValueError("release metadata must be a bounded regular file")
    return json.loads(path.read_text())


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True)+"\n")


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream,"sha256").hexdigest()


def run(name, *args, env=None):
    subprocess.run([os.sys.executable,str(SCRIPTS/name),*map(str,args)],check=True,env=env)


def download(record, destination):
    url = record["url"]
    if not re.fullmatch(r"https://github\.com/jinyongp/loki/releases/download/v0\.2\.(?:0|[1-9][0-9]*)/[a-zA-Z0-9._-]+",url) or not SHA.fullmatch(record["sha256"]) or not isinstance(record["bytes"],int) or not 0 < record["bytes"] <= 8 << 30:
        raise ValueError("reuse requires an owned immutable asset receipt")
    destination.parent.mkdir(parents=True,exist_ok=True)
    if destination.exists():
        if destination.is_symlink() or destination.stat().st_size != record["bytes"] or digest(destination) != record["sha256"]:
            raise ValueError("local reusable bytes differ from the release receipt")
        return destination
    partial = destination.with_name(destination.name+".part")
    try:
        with urlopen(url,timeout=30) as response,partial.open("xb") as out:
            total = 0
            if not response.url.startswith("https://"):
                raise ValueError("asset redirect weakened HTTPS")
            while chunk:=response.read(1024*1024):
                total += len(chunk)
                if total > record["bytes"]:
                    raise ValueError("reused asset exceeded its declared length")
                out.write(chunk)
        if total != record["bytes"] or digest(partial) != record["sha256"]:
            raise ValueError("reused asset differs from its immutable digest")
        partial.rename(destination)
    finally:
        partial.unlink(missing_ok=True)
    return destination


def latest_baseline():
    raw = subprocess.run(["gh","api",f"repos/{CONFIG['repository']}/releases/latest"],capture_output=True,text=True)
    if raw.returncode:
        raise ValueError("could not inspect the last public release: "+raw.stderr)
    release = json.loads(raw.stdout)
    if release.get("draft") or release.get("prerelease") or not release.get("immutable"):
        raise ValueError("baseline must be an immutable stable release")
    asset = next((a for a in release["assets"] if a["name"]=="loki-release-lock.json"),None)
    if asset is None:
        return None
    expected = asset.get("digest","")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}",expected):
        raise ValueError("public baseline lock has no immutable GitHub asset digest")
    with tempfile.TemporaryDirectory(prefix="loki-release-baseline-") as temporary:
        path = download({"url":asset["browser_download_url"],"bytes":asset["size"],"sha256":expected[7:]},Path(temporary)/"lock.json")
        result = read(path)
    if "v"+result["release"] != release["tag_name"]:
        raise ValueError("baseline lock identifies a different release")
    result["published_assets"] = [{"url":a["browser_download_url"],"bytes":a["size"],"sha256":a["digest"].removeprefix("sha256:")} for a in release["assets"]]
    return result


def validate_source(expected, current):
    if expected and (not re.fullmatch(r"[a-f0-9]{40}",expected) or expected != current):
        raise ValueError("dispatch source differs from the locally validated commit")


def outputs(plan):
    path = os.environ.get("GITHUB_OUTPUT")
    if path:
        with open(path,"a") as out:
            out.write("version="+plan["release"]+"\nsource="+plan["source_commit"]+"\n")
            for key,items in plan["matrices"].items():
                out.write(key+"="+json.dumps({"include":items},separators=(",",":"))+"\n")
                out.write(key+"-count="+str(len(items))+"\n")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary,"a") as out:
            out.write("## Selected release jobs\n\n| Component | Action | Reason |\n|---|---|---|\n")
            for key,unit in plan["units"].items():
                out.write(f"| {key} | {unit['action']} | {unit['reason']} |\n")


def native():
    system = {"Linux":"linux","Darwin":"darwin","Windows":"windows"}.get(platform.system())
    arch = {"x86_64":"amd64","amd64":"amd64","aarch64":"arm64","arm64":"arm64"}.get(platform.machine().lower())
    if not system or not arch:
        raise ValueError("unsupported native execution target")
    return system,arch


def environment(plan, mode):
    system,arch = native()
    versions = {u["owner"]:u["version"] for u in plan["units"].values() if u["kind"]=="module" and u["target"]=={"os":system,"arch":arch,"mode":mode}}
    values = {"LOKI_RELEASE":plan["release"],"LOKI_ARTIFACT_VERSIONS":json.dumps(versions,separators=(",",":"))}
    if os.environ.get("GITHUB_ENV"):
        with open(os.environ["GITHUB_ENV"],"a") as out:
            for key,value in values.items():
                out.write(key+"="+value+"\n")
    return dict(os.environ,**values)


def source_check(plan, unit):
    if unit.get("closure"):
        from plan_release import go_closure
        packages = ["./cmd/loki-manager"] if unit["kind"]=="manager" else ["./cmd/loki"] if unit["owner"]=="runtime-core" else ["./cmd/launcher","./cmd/executor"]
        if unit["kind"] == "manager" and unit["target"]["os"] == "windows":
            packages.append("./cmd/loki-keepalive")
        if go_closure(packages,unit["target"]["os"],unit["target"]["arch"]) != unit["closure"]:
            raise ValueError("native source dependency closure differs from the selected plan")


def unit_result(key, unit, artifact=None, manifest=None, image=None):
    result = {k:v for k,v in unit.items() if k not in {"previous","action","validate","reason"}}
    result["producer"] = {"source_commit":os.environ.get("GITHUB_SHA",subprocess.check_output(["git","rev-parse","HEAD"],cwd=ROOT,text=True).strip()),"run_id":os.environ.get("GITHUB_RUN_ID","local"),"job":os.environ.get("GITHUB_JOB","local"),"native_target":unit["target"]}
    if artifact:
        result["artifact"] = artifact
    if manifest:
        result["manifest"] = manifest
    if image:
        result["image"] = image
    return {key:result}


def manager(plan, output):
    system,arch = native();key=f"manager:{system}:{arch}";unit=plan["units"][key]
    source_check(plan,unit)
    env=environment(plan,"project-host")
    run("build_manager_bundle.py","--output",output,env=env)
    receipt=read(output/"manager-receipt.json")
    record={"url":f"https://github.com/{CONFIG['repository']}/releases/download/v{unit['version']}/{receipt['archive']}","sha256":receipt["archive_sha256"],"bytes":receipt["archive_bytes"],"receipt":receipt}
    write(output/"units.json",unit_result(key,unit,artifact=record))


def catalog_result(plan, catalog, directory):
    result={}
    for artifact in catalog["artifacts"]:
        t=artifact["target"];key=f"module:{artifact['module']}:{t['os']}:{t['arch']}:{t['mode']}"
        unit=plan["units"][key];manifest=next(m for m in catalog["modules"] if m["id"]==artifact["module"])
        path=directory/Path(artifact["url"]).name
        if digest(path)!=artifact["sha256"] or path.stat().st_size!=artifact["bytes"] or manifest["release"]!=unit["version"] or manifest.get("contract")!=CONTRACT:
            raise ValueError("built module differs from its selected unit or archive receipt")
        result.update(unit_result(key,unit,artifact=artifact,manifest=manifest))
    return result


def browser(plan, output):
    system,arch=native();target={"os":system,"arch":arch,"mode":"project-host"}
    env=environment(plan,"project-host");output.mkdir(parents=True)
    trust=ROOT/f"packaging/tools/inputs/browser-{system}-{arch}-project-host.json"
    run("acquire_browser_inputs.py","--trust",trust,"--output",output/"inputs","--mode","project-host",env=env)
    unit=plan["units"][f"module:browser:{system}:{arch}:project-host"]
    url=f"https://github.com/{CONFIG['repository']}/releases/download/v{unit['version']}/loki-browser-{unit['version']}-{system}-{arch}-project-host.zip"
    run("build_browser_bundle.py","--inputs",output/"inputs/browser-inputs.json","--output",output/"assets","--release-url",url,env=env)
    catalog=read(output/"assets/browser-catalog.json")
    write(output/"units.json",catalog_result(plan,catalog,output/"assets"))
    shutil.rmtree(output/"inputs")


def unpack_payload(unit, output, downloads):
    artifact=unit["artifact"];archive=download(artifact,downloads/Path(artifact["url"]).name)
    with zipfile.ZipFile(archive) as packed:
        receipt=json.loads(packed.read("prepared-payload-receipt.json"))
        if receipt["module"]!=unit["owner"] or receipt["release"]!=unit["version"] or receipt["target"]!=unit["target"]:
            raise ValueError("reused payload identity differs from the previous immutable unit")
        root=output/"payload";root.mkdir(parents=True)
        for name,expected in receipt["files"].items():
            relative=Path(name)
            if relative.is_absolute() or ".." in relative.parts or "\\" in name:
                raise ValueError("payload file leaves its module")
            data=packed.read(name)
            if name=="full-runtime.json":
                payload=json.loads(data);payload["images"]={}
                data=(json.dumps(payload,indent=2,sort_keys=True)+"\n").encode()
            if len(data)!=expected["bytes"] or hashlib.sha256(data).hexdigest()!=expected["sha256"]:
                raise ValueError("reused payload bytes differ from the original prepared receipt")
            path=root/relative;path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(data);path.chmod(0o755 if expected["executable"] else 0o644)
    write(output/"payload-receipt.json",receipt)
    return output


def public_image(image):
    def clean(value):
        if isinstance(value,dict):
            return {k:(Path(v).name if k=="archive" and isinstance(v,str) else clean(v)) for k,v in value.items()}
        if isinstance(value,list):
            return [clean(v) for v in value]
        return value
    result=clean(image)
    for key in ("archive","bytes","sha256","accepted","published"):
        result.pop(key,None)
    return result


def full(plan, output):
    system,arch=native()
    if system!="linux":
        raise ValueError("full preparation must use its native Linux runner")
    job=next(item for item in plan["matrices"]["full"] if item["arch"]==arch)
    env=environment(plan,"full");output.mkdir(parents=True)
    reused={};images={}
    needed={owner for role in job["roles"] for owner in ROLES[role]}
    for owner in needed-set(job["modules"]):
        old=plan["units"][f"module:{owner}:linux:{arch}:full"]["previous"]
        reused[owner]=str(unpack_payload(old,output/"reuse"/owner,output/"downloads").resolve())
    for role in ROLES:
        unit=plan["units"][f"image:{role}:linux:{arch}"]
        if unit["action"]=="reuse":
            images[role]=unit["previous"]["image"]
    for owner in job["modules"]:
        unit = plan["units"][f"module:{owner}:linux:{arch}:full"]
        source_check(plan, dict(unit, closure=(unit.get("payload") or {}).get("closure")))
    request={"recipe":str(ROOT/f"packaging/tools/inputs/candidate-linux-{arch}-full.json"),"output":str((output/"candidate").resolve()),"selected":job["modules"],"reused_payloads":reused,"image_roles":job["roles"],"reused_images":images}
    write(output/"request.json",request)
    run("release_pipeline.py","prepare-full","--request",output/"request.json",env=env)
    candidate=output/"candidate";results={};assets=output/"assets";assets.mkdir()
    for owner in job["modules"]:
        directory=candidate/"modules"/owner
        catalog=read(directory/("browser-catalog.json" if owner=="browser" else "catalog.json"))
        for artifact in catalog["artifacts"]:
            archive=directory/Path(artifact["url"]).name
            shutil.move(str(archive),assets/archive.name)
        results.update(catalog_result(plan,catalog,assets))
    if job["roles"]:
        from registry_images import publish
        document=publish(candidate/"images")
        for role in job["roles"]:
            key=f"image:{role}:linux:{arch}"
            results.update(unit_result(key,plan["units"][key],image=public_image(document["images"][role])))
    write(output/"units.json",results)
    for receipt_path in (candidate/"native-closures").glob("*/*-receipt.json"):
        receipt = read(receipt_path)
        source = receipt_path.parent/receipt["archive"]
        if source.stat().st_size != receipt["bytes"] or digest(source) != receipt["sha256"]:
            raise ValueError("native public input closure differs from its producer receipt")
        shutil.move(str(source), assets/source.name)
    # Only immutable ZIPs and metadata cross the Actions job boundary.
    shutil.rmtree(candidate);shutil.rmtree(output/"reuse",ignore_errors=True);shutil.rmtree(output/"downloads",ignore_errors=True);(output/"request.json").unlink()


def combine(plan, built):
    units={}
    for path in built.rglob("units.json"):
        for key,value in read(path).items():
            if key not in plan["units"] or key in units or plan["units"][key]["action"]!="build":
                raise ValueError("duplicate/unselected candidate unit")
            expected=plan["units"][key]
            if any(value.get(field)!=expected.get(field) for field in ("fingerprint","validation","version","kind","target")) or value["producer"]["source_commit"]!=plan["source_commit"]:
                raise ValueError("candidate differs from its selected source/input basis")
            units[key]=value
    for key,unit in plan["units"].items():
        if unit["action"]=="reuse":
            old=unit["previous"]
            if not old.get("producer") or not old.get("acceptance") or old.get("version")!=unit["version"]:
                raise ValueError("reuse lacks original production/acceptance evidence")
            units[key]=old
        elif key not in units:
            raise ValueError("selected build result is missing: "+key)
    return {"schema":2,"release":plan["release"],"contract":CONTRACT,"source_commit":plan["source_commit"],"units":units,"installer_fingerprint":plan["installer_fingerprint"],"unmapped_inputs":plan.get("unmapped_inputs",[])}


def archive_in(built, record):
    names=list(built.rglob(Path(record["url"]).name))
    if len(names)!=1:
        raise ValueError("new candidate must have one exact local archive")
    path=names[0]
    if path.is_symlink() or path.stat().st_size!=record["bytes"] or digest(path)!=record["sha256"]:
        raise ValueError("candidate archive differs from its unit receipt")
    return path


def assemble(plan, built, output):
    lock=combine(plan,built);assets=output/"assets";assets.mkdir(parents=True)
    if plan.get("publication_recovery"):
        for record in plan["publication_recovery"]:
            download(record,assets/Path(record["url"]).name)
        existing=read(assets/"loki-release-lock.json")
        if existing["source_commit"] != plan["source_commit"]:
            raise ValueError("published recovery source differs")
        pages=output/"pages";pages.mkdir()
        for extension in ("sh","ps1"):
            shutil.copyfile(assets/("loki-install."+extension),pages/("install."+extension))
        (pages/".nojekyll").write_text("")
        (pages/"index.html").write_text('<!doctype html><title>Loki</title><p>Install Loki with install.sh or install.ps1.</p>')
        return existing
    for source in built.rglob("assets/*.tar.gz"):
        if (assets/source.name).exists():
            raise ValueError("duplicate native input closure")
        shutil.copyfile(source,assets/source.name)
    for key,unit in lock["units"].items():
        if unit["kind"]=="image":
            continue
        record=unit["artifact"]
        if plan["units"][key]["action"]=="build":
            archive=archive_in(built,record);shutil.copyfile(archive,assets/archive.name)
        elif unit["kind"]=="manager":
            download(record,assets/Path(record["url"]).name)
    for system,arch,_ in TARGETS:
        for mode in ("project-host","full"):
            selected=[u for u in lock["units"].values() if u["kind"]=="module" and u["target"]=={"os":system,"arch":arch,"mode":mode}]
            if not selected:
                continue
            write(assets/f"loki-catalog-{system}-{arch}-{mode}.json",{"schema":1,"contract":CONTRACT,"release":lock["release"],"modules":[u["manifest"] for u in selected],"artifacts":[u["artifact"] for u in selected]})
    write(assets/"loki-release-lock.json",lock)
    (assets/"loki-release-notes.md").write_text("Native Loki CLI with independently versioned optional tools. Install the CLI, then select tools using `loki tools`.\n\nWindows: `irm https://jinyongp.dev/loki/install.ps1 | iex`\n\nLinux/macOS: `curl -fsSL https://jinyongp.dev/loki/install.sh | sh`\n\nUse `loki upgrade` to update the CLI while retaining installed tools and settings.\n\nNative acceptance covers the CLI, independent Playwright/Chrome DevTools engines and confined full workspace lifecycle and revocation. Other full job/network/signing/endpoint/sharing combinations, experimental browser workflows and Windows desktop SSH image rendering retain separate product acceptance gates.\n")
    from render_public_installers import render
    render(assets,output/"pages",lock["release"])
    return lock


def materialize(plan, built, output, mode):
    lock=read(built/"assets/loki-release-lock.json");system,arch=native();target={"os":system,"arch":arch,"mode":mode}
    manager=lock["units"][f"manager:{system}:{arch}"];record=manager["artifact"]
    out=output/"manager";out.mkdir(parents=True)
    sources=list(built.rglob(Path(record["url"]).name))
    source=sources[0] if len(sources)==1 else built/"absent"
    if len(sources)>1:
        raise ValueError("duplicate manager archive")
    if source.exists():
        if source.stat().st_size!=record["bytes"] or digest(source)!=record["sha256"]:
            raise ValueError("assembled manager differs")
        shutil.copyfile(source,out/source.name)
    else:
        download(record,out/Path(record["url"]).name)
    write(out/"manager-receipt.json",record["receipt"])
    if mode=="manager":
        return out
    catalog=read(built/f"assets/loki-catalog-{system}-{arch}-{mode}.json")
    directory=output/"release";directory.mkdir();write(directory/"catalog.json",catalog)
    for artifact in catalog["artifacts"]:
        sources=list(built.rglob(Path(artifact["url"]).name))
        if len(sources)>1:
            raise ValueError("duplicate module archive")
        source=sources[0] if sources else built/"absent"
        destination=directory/"archives"/(artifact["sha256"]+".zip");destination.parent.mkdir(exist_ok=True)
        if source.exists():
            if source.stat().st_size!=artifact["bytes"] or digest(source)!=artifact["sha256"]:
                raise ValueError("assembled module differs")
            shutil.copyfile(source,destination)
        else:
            download(artifact,destination)
    if mode=="full":
        images={u["role"]:u["image"] for u in lock["units"].values() if u["kind"]=="image" and u["target"]==target}
        write(output/"images/images.json",{"schema":1,"release":lock["release"],"target":target,"images":images})
    write(output/"candidate.json",{"schema":1,"release":lock["release"],"target":target,"manager":"manager","catalog":"release/catalog.json","images":"images/images.json" if mode=="full" else None})
    return output


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action",choices=["plan","environment","manager","browser","full","prepare-full","assemble","materialize"])
    parser.add_argument("--plan",type=Path);parser.add_argument("--version");parser.add_argument("--force",action="store_true");parser.add_argument("--baseline",type=Path)
    parser.add_argument("--output",type=Path);parser.add_argument("--built",type=Path);parser.add_argument("--request",type=Path);parser.add_argument("--mode",default="project-host")
    args=parser.parse_args()
    if args.action=="plan":
        validate_source(os.environ.get("EXPECTED_SHA", ""), os.environ.get("GITHUB_SHA", ""))
        baseline=read(args.baseline) if args.baseline else latest_baseline()
        release=args.version or "0.2."+str(int((baseline or {"release":CONFIG["version"]})["release"].split(".")[-1])+1)
        result=make_plan(release,baseline,args.force)
        previous_version = (baseline or CONFIG)["release" if baseline else "version"]
        if int(release.split(".")[-1]) < int(previous_version.split(".")[-1]):
            raise ValueError("release version is older than the public baseline")
        if release == previous_version:
            if baseline and result["source_commit"] == baseline["source_commit"] and not args.force:
                result["publication_recovery"] = baseline["published_assets"]
            elif os.environ.get("PUBLISH_REQUEST") == "true" or baseline is None:
                raise ValueError("an existing version requires its exact published source; select the next patch")
            else:
                result["validation_only"] = True
        write(args.output,result);outputs(result)
    elif args.action=="prepare-full":
        request=read(args.request)
        from prepare_candidate import prepare
        prepare(Path(request.pop("recipe")),Path(request.pop("output")),include_manager=False,**request)
    else:
        p=read(args.plan)
        if args.action=="environment":
            environment(p,args.mode)
        elif args.action=="manager":
            manager(p,args.output)
        elif args.action=="browser":
            browser(p,args.output)
        elif args.action=="full":
            full(p,args.output)
        elif args.action=="assemble":
            assemble(p,args.built,args.output)
        elif args.action=="materialize":
            materialize(p,args.built,args.output,args.mode)


if __name__=="__main__":
    main()
