// The pinned Puppeteer engine pauses extension service workers on attachment.
// Resume their initial execution before an action can depend on its listener.
import fs from 'node:fs/promises';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

const entry = process.argv[2];
process.argv.splice(1, 1);
const {puppeteer} = await import(pathToFileURL(path.resolve(path.dirname(entry), '..', 'third_party', 'index.js')));
const {McpContext} = await import(pathToFileURL(path.resolve(path.dirname(entry), '..', 'McpContext.js')));
const triggerAction = McpContext.prototype.triggerExtensionAction;
McpContext.prototype.triggerExtensionAction = async function(id) {
  // The selected MCP page need not be Chrome's active tab (for example when
  // DevTools opens automatically). Extension actions receive the active tab.
  await this.getSelectedMcpPage().pptrPage.bringToFront();
  return triggerAction.call(this, id);
};
const launch = puppeteer.launch.bind(puppeteer);
puppeteer.launch = async options => {
  const browser = await launch(options);
  const resume = async target => {
    if (target.type() !== 'service_worker' || !target.url().startsWith('chrome-extension://')) return;
    const session = await target.createCDPSession();
    try {
      await session.send('Runtime.runIfWaitingForDebugger');
    } finally {
      // The engine can detach/recreate its worker session during reload.
      // Cleanup of our borrowed session must not undo a successful startup.
      await session.detach().catch(() => {});
    }
  };
  const install = browser.installExtension.bind(browser);
  browser.installExtension = async (directory, ...args) => {
    // Reload keeps the extension ID but replaces its worker. Do not attach to
    // the old target while Chrome is still delivering its destruction event.
    const previous = new Set(browser.targets());
    const id = await install(directory, ...args);
    const manifest = JSON.parse(await fs.readFile(path.join(directory, 'manifest.json'), 'utf8'));
    if (manifest.background?.service_worker) {
      const target = await browser.waitForTarget(target => !previous.has(target) && target.type() === 'service_worker' && target.url().startsWith(`chrome-extension://${id}/`), {timeout: 10000});
      await resume(target);
      const worker = await target.worker();
      if (!worker) throw new Error(`Extension ${id} did not start its service worker`);
      await worker.evaluate(() => true);
    }
    return id;
  };
  return browser;
};

await import(pathToFileURL(entry));
