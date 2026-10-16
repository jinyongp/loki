// Adapt user annotations to a headless/remote execution host. The page and
// screenshots remain owned by this MCP session; no system browser is launched.
const crypto = require('node:crypto');
const http = require('node:http');
const entry = process.argv[2];
// Match the CLI's module resolution exactly. Windows short and long paths can
// produce separate CommonJS instances even when they refer to the same file.
const core = require('node:module').createRequire(entry)('playwright-core/lib/coreBundle');
process.argv.splice(1, 3, entry);
const sessions = new WeakMap();
const tool = core.tools.browserTools.find(tool => tool.schema.name === 'browser_annotate');
if (!tool) throw new Error('Pinned Playwright annotation tool is unavailable');

// The pinned recorder treats a file input's change event as a text fill, and
// emits a dismiss placeholder for every dialog signal. Keep the original action
// ordering but capture the actual MCP upload paths and dialog outcomes.
const recordings = new WeakMap();
function recordedUploadCode(action, language) {
  const javascript = ['typescript', 'javascript'].includes(language);
  if (!action.files || !javascript && language !== 'python') return;
  // Recognize the complete statement emitted by the pinned generator from its
  // action metadata. Never search for a method name inside paths or selectors,
  // and retain the surrounding popup/download scope and result declarations.
  const locator = core.iso.asLocator(javascript ? 'javascript' : 'python', action.data.selector);
  const text = core.iso.escapeWithQuotes(action.data.text, javascript ? "'" : '"');
  const fill = `${locator}.fill(${text})${javascript ? ';' : ''}`;
  const prefix = javascript ? /^([ \t]*await page\d*\.)(.*)$/ : /^([ \t]*page\d*\.)(.*)$/;
  let matches = 0;
  const code = action.code.split('\n').map(line => {
    const statement = line.match(prefix);
    if (!statement || statement[2] !== fill) return line;
    ++matches;
    return `${statement[1]}${locator}.${javascript ? 'setInputFiles' : 'set_input_files'}(${JSON.stringify(action.files)})${javascript ? ';' : ''}`;
  }).join('\n');
  // An upstream codegen change must produce a warning, not guessed replay code.
  return matches === 1 ? code : undefined;
}
const browserTool = name => {
  const value = core.tools.browserTools.find(tool => tool.schema.name === name);
  if (!value) throw new Error(`Pinned Playwright tool ${name} is unavailable`);
  return value;
};
const headerSnapshot = core.tools.Tab.prototype.headerSnapshot;
core.tools.Tab.prototype.headerSnapshot = async function() {
  // A dialog can block JavaScript in its opener/popup too. Upstream races only
  // this tab's modal state, so querying another tab's title can strand the MCP
  // response that the client needs before it can handle the dialog.
  if (this.context.tabs().some(tab => tab.modalStates().some(state => state.type === 'dialog'))) {
    return {...this._lastHeader, url: this.page.url(), current: this.isCurrentTab(), changed: true};
  }
  return headerSnapshot.call(this);
};
const resumeTool = browserTool('browser_resume');
const resumeExecution = resumeTool.handle;
resumeTool.handle = async (context, params, response) => {
  if (params.step || params.location) return resumeExecution(context, params, response);
  // The pinned handler waits for another pause even after an ordinary resume.
  // A script that finishes without pausing again leaves its MCP call hanging.
  const browserContext = await context.ensureBrowserContext();
  await browserContext.debugger.resume();
  response.addTextResult('Script execution resumed.');
};
const recordingStart = browserTool('browser_start_recording');
const startRecording = recordingStart.handle;
recordingStart.handle = async (context, params, response) => {
  const browserContext = await context.ensureBrowserContext();
  const start = browserContext._startRecording;
  const state = {actions: [], dialogs: new Map(), upload: undefined};
  browserContext._startRecording = function(options, sink) {
    const action = (page, data, code) => {
      const files = state.upload?.page === page ? state.upload.files : undefined;
      const fileInput = data.name === 'fill' && (files || data.text?.startsWith('C:\\fakepath\\'))
        ? page.locator(data.selector).evaluate(element => element.tagName === 'INPUT' && element.type === 'file', undefined, {timeout: 1000}).catch(() => undefined)
        : Promise.resolve(false);
      return {page, data, code, files, fileInput, dialog: false};
    };
    return start.call(this, options, {
      actionAdded(page, data, code) {state.actions.push(action(page, data, code));sink.actionAdded(page, data, code);},
      actionUpdated(page, data, code) {
        const updated = action(page, data, code);
        // Upstream merges the previous action's signals, but sends only action
        // metadata here. Retain the dialog signal when that action is updated.
        const last = state.actions.at(-1);
        updated.dialog = last?.page === page && last.dialog;
        if (state.actions.length) state.actions[state.actions.length - 1] = updated;else state.actions.push(updated);
        sink.actionUpdated(page, data, code);
      },
      signalAdded(page, signal, code) {
        const last = state.actions.at(-1);
        if (last?.page === page && code) {
          last.code = code;
          if (signal.name === 'dialog') last.dialog = true;
        }
        sink.signalAdded(page, signal, code);
      },
    });
  };
  try {
    await startRecording(context, params, response);
    recordings.set(context, state);
  } finally {
    browserContext._startRecording = start;
  }
};
const uploadTool = browserTool('browser_file_upload');
const uploadFiles = uploadTool.handle;
uploadTool.handle = async (context, params, response, signal) => {
  const state = recordings.get(context);
  if (!state || !params.paths) return uploadFiles(context, params, response, signal);
  const tab = await context.ensureTab();
  state.upload = {page: tab.page, files: await Promise.all(params.paths.map(file => response.resolveClientFilename(file)))};
  try {return await uploadFiles(context, params, response, signal);} finally {state.upload = undefined;}
};
const dialogTool = browserTool('browser_handle_dialog');
const handleDialog = dialogTool.handle;
dialogTool.handle = async (context, params, response, signal) => {
  const state = recordings.get(context);
  const tab = state && await context.ensureTab();
  const modal = tab?.modalStates().find(value => value.type === 'dialog');
  if (!modal) return handleDialog(context, params, response, signal);
  const dialog = modal.dialog;
  const original = params.accept ? dialog.accept : dialog.dismiss;
  const method = params.accept ? 'accept' : 'dismiss';
  dialog[method] = async function(...args) {
    const result = await original.apply(this, args);
    const outcomes = state.dialogs.get(tab.page) || [];
    outcomes.push({accept: params.accept, promptText: params.promptText});
    state.dialogs.set(tab.page, outcomes);
    return result;
  };
  try {return await handleDialog(context, params, response, signal);} finally {dialog[method] = original;}
};
const recordingStop = browserTool('browser_stop_recording');
const stopRecording = recordingStop.handle;
recordingStop.handle = async (context, params, response) => {
  const state = recordings.get(context);
  if (!state) return stopRecording(context, params, response);
  const stop = context.stopRecording;
  const warnings = [];
  context.stopRecording = async function() {
    const result = await stop.call(this);
    if (!result) return result;
    const language = context.codegenLanguage();
    const javascript = ['typescript', 'javascript'].includes(language);
    await Promise.all(state.actions.map(async action => {action.fileInput = await action.fileInput;}));
    const dialogCounts = new Map();
    const dialogIndexes = new Map();
    for (const action of state.actions) {
      if (action.dialog) dialogCounts.set(action.page, (dialogCounts.get(action.page) || 0) + 1);
    }
    return state.actions.map(action => {
      let code = action.code;
      // Consume each action's dialog outcome even when the upload is omitted.
      // Otherwise the next retained action would replay the previous answer.
      let outcome;
      if (action.dialog) {
        const outcomes = state.dialogs.get(action.page) || [];
        const index = dialogIndexes.get(action.page) || 0;
        dialogIndexes.set(action.page, index + 1);
        if (outcomes.length !== dialogCounts.get(action.page) || !javascript) {
          warnings.push('A recorded dialog action was omitted: its actual outcome could not be recovered in this code language.');
          return '';
        }
        outcome = outcomes[index];
      }
      if (action.fileInput !== false) {
        code = action.fileInput === true ? recordedUploadCode(action, language) : undefined;
        if (code === undefined) {
          warnings.push('A recorded file upload was omitted: replay requires actual host paths and a supported code language.');
          return '';
        }
      }
      if (outcome?.accept) code = code.replace('dialog.dismiss()', () => `dialog.accept(${outcome.promptText === undefined ? '' : JSON.stringify(outcome.promptText)})`);
      return code;
    }).filter(code => code.trim());
  };
  try {
    await stopRecording(context, params, response);
    for (const warning of new Set(warnings)) response.addTextResult(warning);
  } finally {
    context.stopRecording = stop;
    recordings.delete(context);
  }
};

const html = `<!doctype html><html lang="en"><meta charset="utf-8"><title>Loki page annotations</title>
<meta name="viewport" content="width=device-width, initial-scale=1"><style>
body{font:16px system-ui;margin:24px;background:#101827;color:#eef2ff}main{max-width:1280px;margin:auto}
canvas{max-width:100%;height:auto;display:block;touch-action:none;cursor:crosshair;background:white}
button,input,textarea{font:inherit;padding:8px;margin:6px 6px 12px 0}textarea{display:block;width:95%;max-width:700px}
#message{min-height:1.5em;color:#c4b5fd}#marks{padding-left:24px}button:focus-visible,input:focus-visible,textarea:focus-visible{outline:3px solid #c4b5fd}
</style><main><h1>Annotate the captured page</h1>
<p>Drag a box over the screenshot. Add a label before drawing, or use the feedback field. Submit when done, then call browser_annotate again in your MCP client to retrieve the result.</p>
<label>Box label <input id="label" maxlength="2000"></label><button id="undo">Undo last box</button>
<canvas id="canvas" aria-label="Captured page; drag to mark a rectangular area"></canvas><ol id="marks" aria-label="Annotations"></ol>
<label for="feedback">Feedback</label><textarea id="feedback" maxlength="4000" rows="3"></textarea>
<button id="submit">Submit annotations</button><button id="cancel">Cancel</button><p id="message" role="status"></p></main>
<script>
const canvas=document.querySelector('#canvas'),ctx=canvas.getContext('2d'),image=new Image();let annotations=[],start,preview,finished=false;
const message=text=>document.querySelector('#message').textContent=text;
function render(){ctx.clearRect(0,0,canvas.width,canvas.height);ctx.drawImage(image,0,0);for(const a of [...annotations,...(preview?[preview]:[])]){ctx.strokeStyle='#ec4899';ctx.lineWidth=3;ctx.strokeRect(a.x,a.y,a.width,a.height);if(a.label){ctx.font='16px sans-serif';ctx.fillStyle='#ec4899';ctx.fillRect(a.x,Math.max(0,a.y-24),Math.min(ctx.measureText(a.label).width+12,canvas.width-a.x),24);ctx.fillStyle='white';ctx.fillText(a.label,a.x+6,Math.max(18,a.y-6));}}}
function point(e){const r=canvas.getBoundingClientRect();return {x:Math.max(0,Math.min(canvas.width,(e.clientX-r.left)*canvas.width/r.width)),y:Math.max(0,Math.min(canvas.height,(e.clientY-r.top)*canvas.height/r.height))};}
function box(p){return {x:Math.min(start.x,p.x),y:Math.min(start.y,p.y),width:Math.abs(p.x-start.x),height:Math.abs(p.y-start.y),label:document.querySelector('#label').value};}
function list(){const marks=document.querySelector('#marks');marks.replaceChildren();for(const a of annotations){const item=document.createElement('li');item.textContent=(a.label||'Unlabelled box')+' ('+Math.round(a.x)+', '+Math.round(a.y)+')';marks.append(item);}}
image.onload=()=>{canvas.width=image.naturalWidth;canvas.height=image.naturalHeight;render();};image.onerror=()=>message('Could not load the captured screenshot. Start a new annotation session.');image.src='image';
canvas.onpointerdown=e=>{if(finished||!image.complete)return;start=point(e);canvas.setPointerCapture(e.pointerId);};
canvas.onpointermove=e=>{if(!start)return;preview=box(point(e));render();};
canvas.onpointerup=e=>{if(!start)return;const a=box(point(e));start=null;preview=null;if(a.width>=2&&a.height>=2&&annotations.length<100)annotations.push(a);render();list();};
canvas.onpointercancel=()=>{start=null;preview=null;render();};
document.querySelector('#undo').onclick=()=>{if(!finished){annotations.pop();render();list();}};
async function send(body){try{const response=await fetch('submit',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!response.ok)throw Error(await response.text());finished=true;document.querySelector('#submit').disabled=true;message(body.cancel?'Cancelled. Return to your MCP client.':'Submitted. Return to your MCP client and call browser_annotate again.');}catch(e){message(e.message);}}
document.querySelector('#submit').onclick=()=>{if(finished)return;const feedback=document.querySelector('#feedback').value;if(!annotations.length&&!feedback.trim()){message('Draw a box or add feedback before submitting.');return;}preview=null;render();send({annotations,feedback,image:canvas.toDataURL('image/png').split(',')[1]});};
document.querySelector('#cancel').onclick=()=>{if(!finished)send({cancel:true});};
document.addEventListener('keydown',e=>{if(e.key==='Escape'&&!finished)send({cancel:true});});
</script></html>`;

function validateSubmission(value, width, height) {
  if (value.cancel === true) return {cancel: true};
  if (!Array.isArray(value.annotations) || value.annotations.length > 100 || typeof value.feedback !== 'string' || value.feedback.length > 4000 || typeof value.image !== 'string' || value.image.length > 12 << 20) throw new Error('Invalid annotation submission');
  for (const a of value.annotations) {
    if (!a || !['x', 'y', 'width', 'height'].every(key => Number.isFinite(a[key])) || a.x < 0 || a.y < 0 || a.width < 2 || a.height < 2 || a.x + a.width > width + 1 || a.y + a.height > height + 1 || typeof a.label !== 'string' || a.label.length > 2000) throw new Error('Invalid annotation box');
  }
  if (!value.annotations.length && !value.feedback.trim()) throw new Error('Add an annotation or feedback');
  const image = Buffer.from(value.image, 'base64');
  if (image.length < 24 || !image.subarray(0, 8).equals(Buffer.from([137,80,78,71,13,10,26,10])) || image.readUInt32BE(16) !== width || image.readUInt32BE(20) !== height) throw new Error('Invalid annotated PNG');
  return {annotations: value.annotations, feedback: value.feedback, image};
}

tool.schema.description = 'Start a user annotation screen for the current page. Returns a one-time loopback URL on the execution host. Draw boxes or enter feedback there, submit, then call browser_annotate again to receive the annotated PNG and page snapshot. On SSH hosts, forward the displayed port to open it locally. The screen expires after five minutes.';
tool.handle = async (context, params, response) => {
  let session = sessions.get(context);
  if (session?.expired) {
    sessions.delete(context);
    response.addError('Annotation screen expired. Call browser_annotate again to start a new one.');
    return;
  }
  if (session?.result) {
    session.close();sessions.delete(context);
    if (session.result.cancel) {response.addTextResult('Annotation cancelled.');return;}
    await response.addResult('Annotated screenshot', session.result.image, {prefix:'annotations',ext:'png'});
    await response.addResult('Annotation snapshot', session.snapshot, {prefix:'annotations',ext:'yaml'});
    response.addTextResult(JSON.stringify({annotations:session.result.annotations,feedback:session.result.feedback}));
    return;
  }
  if (!session) {
    const tab = await context.ensureTab();
    if (tab.modalStates().length) {
      response.addError('Handle the page dialog before starting an annotation screen.');
      return;
    }
    const image = await tab.page.screenshot({type:'png'});
    const snapshot = await tab.page.locator('body').ariaSnapshot();
    const width = image.readUInt32BE(16), height = image.readUInt32BE(20);
    const token = crypto.randomBytes(24).toString('hex');
    session = {snapshot, image, expired:false};
    const server = http.createServer(async (request, reply) => {
      reply.setHeader('Cache-Control', 'no-store');
      reply.setHeader('X-Content-Type-Options', 'nosniff');
      reply.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'");
      const origin = `http://127.0.0.1:${server.address()?.port}`;
      const prefix = `/${token}/`;
      if (request.headers.host !== `127.0.0.1:${server.address()?.port}` || request.headers.origin && request.headers.origin !== origin) {reply.writeHead(403).end();return;}
      if (session.expired || session.result) {reply.writeHead(410).end('Annotation screen is closed');return;}
      if (request.method === 'GET' && request.url === prefix) {reply.setHeader('Content-Type','text/html; charset=utf-8');reply.end(html);return;}
      if (request.method === 'GET' && request.url === prefix+'image') {reply.setHeader('Content-Type','image/png');reply.end(image);return;}
      if (request.method !== 'POST' || request.url !== prefix+'submit') {reply.writeHead(404).end();return;}
      let size=0;const chunks=[];
      try {
        for await (const chunk of request) {size+=chunk.length;if(size>16<<20)throw new Error('Annotation submission is too large');chunks.push(chunk);}
        session.result=validateSubmission(JSON.parse(Buffer.concat(chunks).toString('utf8')),width,height);
        reply.end('ok');
      } catch (error) {reply.writeHead(400).end(error.message);}
    });
    await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
    server.unref();
    session.url=`http://127.0.0.1:${server.address().port}/${token}/`;
    const timer=setTimeout(()=>{session.expired=true;session.close();},5*60*1000);timer.unref();
    session.close=()=>{clearTimeout(timer);server.close();server.closeAllConnections();server.removeAllListeners('request');session.image=undefined;};
    sessions.set(context,session);
  }
  response.addTextResult(`Open this one-time annotation screen on the execution host: ${session.url}\nDraw annotations or add feedback, submit, then call browser_annotate again. The screen expires after five minutes.`);
};

require(entry);
