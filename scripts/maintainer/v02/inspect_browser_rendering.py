#!/usr/bin/env python3
"""Final-phase native rendering diagnosis of a retained candidate, no publication."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

from accept_manager_bundle import accept as accept_manager
from build_browser_bundle import digest

PROBE = r'''
const { chromium } = require(process.argv[1]);
(async () => {
  const cases = [[], ['--disable-gpu'], ['--use-gl=angle','--use-angle=swiftshader']];
  for (const args of cases) {
    let browser;
    try {
      browser = await chromium.launch({executablePath:process.argv[2], headless:true, chromiumSandbox:true, args, timeout:15000});
      const page = await browser.newPage({viewport:{width:800,height:600}});
      await page.setContent('<h1>Loki native rendering diagnostic</h1>');
      await page.evaluate(() => Promise.race([new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))), new Promise((_,reject) => setTimeout(() => reject(new Error('Frame readiness timeout')),3000))]));
      const png = await page.screenshot({timeout:10000});
      console.log(JSON.stringify({args, png_bytes:png.length, result:'pass'}));
    } catch (error) {console.log(JSON.stringify({args, result:'fail', error:String(error).slice(0,4096)}));}
    finally {if (browser) await browser.close();}
  }
})().catch(error => {console.error(String(error));process.exitCode=1});
'''

def inspect(candidate):
    manager = candidate / 'manager'
    accept_manager(manager)
    receipt = json.loads((manager / 'manager-receipt.json').read_text())
    archive = manager / receipt['archive']
    if digest(archive) != receipt['archive_sha256']:
        raise ValueError('manager receipt mismatch')
    with tempfile.TemporaryDirectory(prefix='loki-browser-render-') as temporary:
        scratch = Path(temporary)
        binary = scratch / receipt['binary']
        with zipfile.ZipFile(archive) as packed:
            binary.write_bytes(packed.read(receipt['binary']))
        if digest(binary) != receipt['binary_sha256']:
            raise ValueError('manager binary receipt mismatch')
        binary.chmod(0o755)
        root = scratch / 'management'
        for args in [['tools','configure','--mode','project-host'],['tools','install','browser','--catalog',str(candidate/'release/catalog.json'),'--archives',str(candidate/'release/archives')]]:
            subprocess.run([str(binary),'--root',str(root),*args],check=True)
        generation = next((root/'tools/browser/generations').iterdir())
        r = json.loads((generation/'runtime.json').read_text())
        environment = dict(os.environ, PLAYWRIGHT_BROWSERS_PATH=str(generation/r['browsers']))
        subprocess.run([str(generation/r['node']),'-e',PROBE,str(generation/'node_modules/playwright-core/index.js'),str(generation/r['chrome'])],env=environment,check=True,timeout=100)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate', type=Path, required=True)
    inspect(parser.parse_args().candidate.resolve())
