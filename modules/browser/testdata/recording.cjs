const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {test} = require('node:test');

const source = fs.readFileSync(process.argv[2], 'utf8');

// Substitute only the pinned codegen helpers in the unit harness. Native
// acceptance exercises these calls against the actual Playwright bundle.
function quote(text, mark) {
  const body = JSON.stringify(text).slice(1, -1).replace(/\\"/g, '"');
  return mark + body.replaceAll(mark, '\\' + mark) + mark;
}
function locator(language, selector) {
  return `locator(${quote(selector, language === 'python' ? '"' : "'")})`;
}
function fillCode(language, text, selector = '#upload') {
  const python = language === 'python';
  return `${python ? '' : 'await '}page.${locator(language, selector)}.fill(${quote(text, python ? '"' : "'")})${python ? '' : ';'}`;
}

async function recorder(language = 'javascript') {
  let sink, output, selectedPage, uploadAction;
  const warnings = [];
  const page = {locator(selector) {return {async evaluate() {
    if (selector === '#missing-upload') throw new Error('Input was removed');
    return selector === '#upload';
  }};}};
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
    browser_file_upload: async () => uploadAction(),
    browser_handle_dialog: async (_context, params) => dialog[params.accept ? 'accept' : 'dismiss'](),
  };
  const browserTools = Object.entries(handlers).map(([name, handle]) => ({schema: {name}, handle}));
  const Tab = class {async headerSnapshot() {return this.readHeader();}};
  const entry = '/fixture/cli.js';
  const mockedRequire = name => {
    if (name === entry) return {};
    if (name === 'node:module') return {createRequire: () => () => ({tools: {browserTools, Tab}, iso: {asLocator: locator, escapeWithQuotes: quote}})};
    return require(name);
  };
  vm.runInNewContext(source, {require: mockedRequire, process: {argv: ['node', 'adapter', entry]}, Buffer});
  const tool = name => browserTools.find(value => value.schema.name === name);
  const response = {addTextResult: message => warnings.push(message), async resolveClientFilename(file) {return file;}};
  await tool('browser_start_recording').handle(context, {}, response);
  return {
    page,
    header(tab) {return Tab.prototype.headerSnapshot.call(tab);},
    action(data, code) {sink.actionAdded(page, data, code);},
    async upload(files, text, code = fillCode(language, text), selector = '#upload') {
      uploadAction = () => sink.actionAdded(page, {name: 'fill', text, selector}, code);
      await tool('browser_file_upload').handle(context, {paths: files}, response);
    },
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

test('tab headers stay responsive while a dialog blocks another tab', async () => {
  const recording = await recorder();
  let blocked = true;
  const tab = {
    context: {tabs() {return [{modalStates() {return blocked ? [{type: 'dialog'}] : [];}}];}},
    _lastHeader: {title: 'Upload popup'},
    page: {url() {return 'about:blank';}},
    isCurrentTab() {return true;},
    async readHeader() {
      if (blocked) throw new Error('Renderer is blocked by the opener dialog');
      return {title: 'Live title'};
    },
  };
  assert.deepEqual({...await recording.header(tab)}, {title: 'Upload popup', url: 'about:blank', current: true, changed: true});
  blocked = false;
  assert.deepEqual(await recording.header(tab), {title: 'Live title'});
});

for (const language of ['javascript', 'typescript', 'python']) {
  for (const name of ['ordinary.txt', 'report.fill(.txt', 'data.fill(test).csv', `quoted '" $&.txt`]) {
    test(`upload replay preserves ${name} in ${language}`, async () => {
      const recording = await recorder(language);
      const files = ['/owned/results/' + name];
      await recording.upload(files, 'C:\\fakepath\\' + name);
      const result = await recording.stop();
      assert.deepEqual(result.warnings, []);
      assert.equal(result.code.length, 1);
      if (language === 'python') {
        assert.equal(result.code[0], `page.locator("#upload").set_input_files(${JSON.stringify(files)})`);
      } else {
        let received;
        const page = {locator() {return {async setInputFiles(value) {received = value;}};}};
        await new (Object.getPrototypeOf(async function() {}).constructor)('page', result.code[0])(page);
        assert.deepEqual(received, files);
      }
    });
  }

  test(`clearing files is recorded as an empty upload in ${language}`, async () => {
    const recording = await recorder(language);
    await recording.upload([], '');
    const result = await recording.stop();
    assert.deepEqual(result.warnings, []);
    assert.match(result.code[0], language === 'python' ? /\.set_input_files\(\[\]\)/ : /\.setInputFiles\(\[\]\)/);
    assert.doesNotMatch(result.code[0], /\.fill\(/);
  });

  test(`ordinary fakepath text stays text during an upload in ${language}`, async () => {
    const recording = await recorder(language);
    const text = 'C:\\fakepath\\ordinary.txt';
    const code = fillCode(language, text, '#textbox');
    await recording.upload(['/owned/ordinary.txt'], text, code, '#textbox');
    assert.deepEqual(await recording.stop(), {code: [code], warnings: []});
  });
}

for (const event of ['popup', 'download']) {
  test(`upload conversion retains the ${event} result and subsequent use`, async () => {
    const recording = await recorder();
    const code = `const resultPromise = page.waitForEvent('${event}');\n${fillCode('javascript', 'C:\\fakepath\\ordinary.txt')}\nconst result = await resultPromise;\nawait result.use();`;
    await recording.upload(['/owned/ordinary.txt'], 'C:\\fakepath\\ordinary.txt', code);
    const output = await recording.stop();
    const calls = [];
    const page = {
      waitForEvent(name) {calls.push(name);return Promise.resolve({async use() {calls.push('used');}});},
      locator() {return {async setInputFiles(files) {calls.push(...files);}};},
    };
    await new (Object.getPrototypeOf(async function() {}).constructor)('page', output.code.join('\n'))(page);
    assert.deepEqual(calls, [event, '/owned/ordinary.txt', 'used']);
    assert.deepEqual(output.warnings, []);
  });
}

test('Python upload conversion retains the popup scope and result', async () => {
  const recording = await recorder('python');
  const text = 'C:\\fakepath\\ordinary.txt';
  const prefix = 'with page.expect_popup() as page1_info:\n    ';
  const suffix = '\npage1 = page1_info.value';
  await recording.upload(['/owned/ordinary.txt'], text, prefix + fillCode('python', text) + suffix);
  assert.deepEqual(await recording.stop(), {
    code: [prefix + 'page.locator("#upload").set_input_files(["/owned/ordinary.txt"])' + suffix], warnings: [],
  });
});

for (const firstAccept of [false, true]) {
  test(`omitting an upload cannot shift the next dialog outcome (${firstAccept})`, async () => {
    const recording = await recorder();
    const text = 'C:\\fakepath\\manual.txt';
    recording.action({name: 'fill', text, selector: '#upload'}, fillCode('javascript', text));
    await recording.dialog("page.once('dialog', dialog => dialog.dismiss());\n" + fillCode('javascript', text), firstAccept, 'first answer');
    recording.add('next');
    await recording.dialog("page.once('dialog', dialog => dialog.dismiss());\nawait page.getByRole('button').click();", !firstAccept, 'second answer');
    const result = await recording.stop();
    assert.equal(result.code.length, 1);
    assert.equal(result.warnings.length, 1);
    let received;
    const page = {
      once(_event, listener) {listener({accept(value) {received = ['accept', value];}, dismiss() {received = ['dismiss'];}});},
      getByRole() {return {async click() {}};},
    };
    await new (Object.getPrototypeOf(async function() {}).constructor)('page', result.code[0])(page);
    assert.deepEqual(received, firstAccept ? ['dismiss'] : ['accept', 'second answer']);
  });
}

test('an unrecognized upload statement is omitted with a warning', async () => {
  const recording = await recorder();
  await recording.upload(['/owned/ordinary.txt'], 'C:\\fakepath\\ordinary.txt', "await customUpload('C:\\\\fakepath\\\\ordinary.txt');");
  const result = await recording.stop();
  assert.deepEqual(result.code, []);
  assert.equal(result.warnings.length, 1);
});

test('an uninspectable input is omitted instead of guessed to be a file input', async () => {
  const recording = await recorder();
  const text = 'C:\\fakepath\\ordinary.txt';
  await recording.upload(['/owned/ordinary.txt'], text, fillCode('javascript', text, '#missing-upload'), '#missing-upload');
  const result = await recording.stop();
  assert.deepEqual(result.code, []);
  assert.equal(result.warnings.length, 1);
});

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
