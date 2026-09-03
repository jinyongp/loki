import { createHash } from 'node:crypto';
import { createServer } from 'node:http';

const port = Number.parseInt(process.argv[2] ?? '', 10);
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error('a valid port is required');
}

const page = `<!DOCTYPE html>
<html><head><title>Loki browser verifier</title></head>
<body><button id="probe">Loki browser verifier</button>
<script src="/asset.js"></script></body></html>`;

const server = createServer((request, response) => {
  if (request.url === '/asset.js') {
    response.writeHead(200, { 'content-type': 'text/javascript' });
    response.end("globalThis.lokiSocket = new WebSocket(`ws://${location.host}/socket`, 'vite-hmr');\n");
    return;
  }
  response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
  response.end(page);
});

server.on('upgrade', (request, socket) => {
  const key = request.headers['sec-websocket-key'];
  if (typeof key !== 'string') {
    socket.destroy();
    return;
  }
  const accept = createHash('sha1')
    .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
    .digest('base64');
  const protocol = String(request.headers['sec-websocket-protocol'] ?? '')
    .split(',').map((value) => value.trim()).find((value) => value === 'vite-hmr');
  socket.write([
    'HTTP/1.1 101 Switching Protocols',
    'Upgrade: websocket',
    'Connection: Upgrade',
    `Sec-WebSocket-Accept: ${accept}`,
    ...(protocol ? [`Sec-WebSocket-Protocol: ${protocol}`] : []),
    '', '',
  ].join('\r\n'));
});

server.listen(port, '127.0.0.1', () => {
  console.log(`Loki preview verifier listening on ${port}`);
});
