const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {test} = require('node:test');

const source = fs.readFileSync(process.argv[2], 'utf8');

async function recorder(language = 'javascript') {
  let sink, output, selectedPage;
  const warnings = [];
  const page = {};
  selectedPage = page;
  const browserContext = {_startRecording(_options, value) {sink = value;}};
  const dialog = {async accept() {}, async dismiss() {}};
  const context = {
    async ensureBrowserContext() {return browserContext;},
    async ensureTab() {return {page: selectedPage, modalStates: () => [{type: 'dialog', dialog}]};},
    codegenLanguage() {return language;},
    async stopRecording() {return ['upstream output'];},
  };
  const handlers = {
    browser_annotate: async () => {},
    browser_resume: async () => {},
    browser_start_recording: async () => browserContext._startRecording({}, {
      actionAdded() {}, actionUpdated() {}, signalAdded() {},
    }),
    browser_stop_recording: async () => {output = await context.stopRecording();},
    browser_file_upload: async () => {},
    browser_handle_dialog: async (_context, params) => dialog[params.accept ? 'accept' : 'dismiss'](),
  };
  const browserTools = Object.entries(handlers).map(([name, handle]) => ({schema: {name}, handle}));
  const entry = '/fixture/cli.js';
  const mockedRequire = name => {
    if (name === entry) return {};
    if (name === 'node:module') return {createRequire: () => () => ({tools: {browserTools}})};
    return require(name);
  };
  vm.runInNewContext(source, {require: mockedRequire, process: {argv: ['node', 'adapter', entry]}, Buffer});
  const tool = name => browserTools.find(value => value.schema.name === name);
  const response = {addTextResult: message => warnings.push(message)};
  await tool('browser_start_recording').handle(context, {}, response);
  return {
    page,
    add(text, target = page) {
      const code = `await page.getByRole('textbox').fill(${JSON.stringify(text)});`;
      sink.actionAdded(target, {name: 'fill', text, selector: 'input'}, code);
      return code;
    },
    async dialog(code, accept, promptText, target = page) {
      sink.signalAdded(target, {name: 'dialog', dialogAlias: '1'}, code);
      selectedPage = target;
      await tool('browser_handle_dialog').handle(context, {accept, promptText}, response);
    },
    update(text, code) {sink.actionUpdated(page, {name: 'fill', text, selector: 'input'}, code);},
    signal(target, code) {sink.signalAdded(target, {name: 'navigation'}, code);},
    async stop() {
      await tool('browser_stop_recording').handle(context, {}, response);
      return {code: Array.from(output), warnings};
    },
  };
}

for (const language of ['javascript', 'typescript', 'python']) {
  test(`literal dialog text is retained in ${language}`, async () => {
    const recording = await recorder(language);
    const expected = ['ordinary text', 'dialog.dismiss()', 'Search for dialog.dismiss() in the source'].map(value => recording.add(value));
    assert.deepEqual(await recording.stop(), {code: expected, warnings: []});
  });
}

test('literal text and real accept/dismiss signals preserve each action', async () => {
  const recording = await recorder();
  const literal = recording.add('dialog.dismiss()');
  recording.add('accept');
  const acceptCode = "page.once('dialog', dialog => dialog.dismiss());\nawait page.getByRole('button').click();";
  await recording.dialog(acceptCode, true, 'Actual response');
  const middle = recording.add('Search for dialog.dismiss() in the source');
  recording.add('dismiss');
  const dismissCode = "page.once('dialog', dialog => dialog.dismiss());\nawait page.getByRole('button').click();";
  await recording.dialog(dismissCode, false);
  assert.deepEqual(await recording.stop(), {
    code: [literal, acceptCode.replace('dialog.dismiss()', 'dialog.accept("Actual response")'), middle, dismissCode],
    warnings: [],
  });
});

test('updating an action retains its dialog signal and literal payload', async () => {
  const recording = await recorder();
  recording.add('before');
  const code = "page.once('dialog', dialog => dialog.dismiss());\nawait page.getByRole('textbox').fill('dialog.dismiss()');";
  await recording.dialog(code, true, 'reply');
  recording.update('dialog.dismiss()', code);
  assert.deepEqual(await recording.stop(), {
    code: [code.replace('dialog.dismiss()', 'dialog.accept("reply")')], warnings: [],
  });
});

test('a signal from another page cannot replace the last action', async () => {
  const recording = await recorder();
  const code = recording.add('keep me');
  recording.signal({}, 'foreign page code');
  assert.deepEqual(await recording.stop(), {code: [code], warnings: []});
});

test('unrecoverable real dialogs omit only their own action', async () => {
  const recording = await recorder('python');
  const literal = recording.add('dialog.dismiss()');
  recording.add('trigger');
  await recording.dialog('page.on("dialog", lambda dialog: dialog.dismiss())', true);
  const result = await recording.stop();
  assert.deepEqual(result.code, [literal]);
  assert.equal(result.warnings.length, 1);
});

for (const promptText of ['$&', "$'", '$`', '$$']) {
  test(`dialog prompt preserves replacement metacharacters ${promptText}`, async () => {
    const recording = await recorder();
    recording.add('trigger');
    const code = "page.once('dialog', dialog => dialog.dismiss());\nawait page.getByRole('button').click();";
    await recording.dialog(code, true, promptText);
    const result = await recording.stop();
    let received;
    const page = {once(_event, listener) {listener({accept(value) {received = value;}});}, getByRole() {return {async click() {}};}};
    await new (Object.getPrototypeOf(async function() {}).constructor)('page', result.code.join('\n'))(page);
    assert.equal(received, promptText);
    assert.deepEqual(result.warnings, []);
  });
}
