const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.argv[2] + '/node_modules/playwright');
const descriptor = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
(async () => {
  const browser = await chromium.connect(descriptor.endpoint ?? descriptor.pipeName);
  const context = browser.contexts()[0];
  const page = context.pages().find(page => page.url() === process.argv[4]);
  if (!page) throw new Error('Owned fixture page not found');
  await context.debugger._enable();
  await context.debugger.requestPause();
  const action = page.locator('#count').click({timeout: 30000});
  action.catch(() => {});
  const deadline = Date.now() + 10000;
  while (!context.debugger.pausedDetails()) {
    if (Date.now() >= deadline) throw new Error('Fixture did not pause');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  console.log('PAUSED');
  await action;
  console.log(JSON.stringify({resumed: true, counter: await page.locator('#counter').textContent(), paused: !!context.debugger.pausedDetails()}));
  process.exit(0);
})().catch(error => {console.error(error);process.exit(1)});
