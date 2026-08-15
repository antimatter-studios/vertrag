#!/usr/bin/env node
//
// The Node.js hooks worker.
//
// Dredd loads Node hook files into its own process, which vertrag cannot do: it
// is a Go program. This worker is the bridge. It runs the user's hook files in
// a real Node process and speaks vertrag's hook protocol over a socket, so a
// project's existing hook file keeps working unchanged — which is the whole
// point, since a hook file addresses transactions by names derived from the
// API description and rewriting it by hand is where mistakes happen.
//
// The protocol is Dredd's own worker protocol: newline-delimited JSON objects
// of {event, uuid, data} in, {uuid, data} out.
//
//   node nodejs.js --port <port> <hookfile>...

const net = require('net');
const path = require('path');
const Module = require('module');

// The hook API, matching Dredd's. Named hooks are held per transaction name;
// the "each" variants apply to all of them.
const named = { before: {}, after: {}, beforeValidation: {} };
const each = { beforeAll: [], afterAll: [], beforeEach: [], afterEach: [], beforeEachValidation: [] };

function addNamed(kind) {
  return (name, hook) => {
    if (!named[kind][name]) named[kind][name] = [];
    named[kind][name].push(hook);
  };
}

function addEach(kind) {
  return hook => each[kind].push(hook);
}

const hooks = {
  before: addNamed('before'),
  after: addNamed('after'),
  beforeValidation: addNamed('beforeValidation'),

  beforeAll: addEach('beforeAll'),
  afterAll: addEach('afterAll'),
  beforeEach: addEach('beforeEach'),
  afterEach: addEach('afterEach'),
  beforeEachValidation: addEach('beforeEachValidation'),

  // Dredd exposes these to hook files. `log` is collected rather than printed
  // directly so it interleaves with vertrag's own output rather than racing it.
  log: message => process.stderr.write(`hook: ${message}\n`),
  configuration: {},
};

// Hook files ask for this module by name — `require('hooks')`. Nothing on disk
// provides it, so it is installed into the module cache before they load.
function installHooksModule() {
  const id = 'hooks';
  const fake = new Module(id, null);
  fake.exports = hooks;
  fake.loaded = true;
  require.cache[id] = fake;

  const original = Module._resolveFilename;
  Module._resolveFilename = function resolve(request, ...rest) {
    if (request === 'hooks') return id;
    return original.call(this, request, ...rest);
  };
}

// runHooks invokes a list of hooks in order, supporting both calling
// conventions Dredd allows: a callback, or a returned promise.
async function runHooks(list, argument) {
  for (const hook of list) {
    // A hook that declares a second parameter wants a callback.
    if (hook.length > 1) {
      await new Promise((resolve, reject) => {
        let settled = false;
        try {
          hook(argument, () => {
            if (!settled) { settled = true; resolve(); }
          });
        } catch (error) {
          if (!settled) { settled = true; reject(error); }
        }
      });
      continue;
    }
    await hook(argument);
  }
}

async function handle(event, data) {
  switch (event) {
    case 'beforeAll':
    case 'afterAll':
      await runHooks(each[event], data);
      return data;

    case 'beforeEach':
      await runHooks(each.beforeEach, data);
      await runHooks(named.before[data.name] || [], data);
      return data;

    case 'beforeEachValidation':
      await runHooks(each.beforeEachValidation, data);
      await runHooks(named.beforeValidation[data.name] || [], data);
      return data;

    case 'afterEach':
      // Named hooks run before the "each" ones on the way out, mirroring the
      // way they run after them on the way in.
      await runHooks(named.after[data.name] || [], data);
      await runHooks(each.afterEach, data);
      return data;

    default:
      return data;
  }
}

function main() {
  const args = process.argv.slice(2);
  let port = 61321;
  const hookfiles = [];

  for (let i = 0; i < args.length; i += 1) {
    if (args[i] === '--port') {
      port = Number(args[i += 1]);
    } else {
      hookfiles.push(args[i]);
    }
  }

  installHooksModule();

  for (const file of hookfiles) {
    require(path.resolve(file));
  }

  const server = net.createServer((socket) => {
    let buffer = '';

    socket.on('data', async (chunk) => {
      buffer += chunk.toString();

      // Messages are newline-delimited; the trailing fragment is whatever has
      // not arrived in full yet.
      const parts = buffer.split('\n');
      buffer = parts.pop();

      for (const part of parts) {
        if (!part.trim()) continue;

        let message;
        try {
          message = JSON.parse(part);
        } catch (error) {
          continue;
        }

        try {
          const data = await handle(message.event, message.data);
          socket.write(`${JSON.stringify({ uuid: message.uuid, data })}\n`);
        } catch (error) {
          // A throwing hook fails its transaction rather than the run: one bad
          // hook should not stop the rest of the suite from reporting.
          socket.write(`${JSON.stringify({
            uuid: message.uuid,
            data: message.data,
            error: (error && error.message) || String(error),
          })}\n`);
        }
      }
    });
  });

  server.listen(port, '127.0.0.1', () => {
    // vertrag waits for this line before sending anything.
    process.stdout.write('vertrag-hooks-ready\n');
  });

  process.on('SIGTERM', () => server.close(() => process.exit(0)));
}

try {
  main();
} catch (error) {
  process.stderr.write(`${(error && error.stack) || error}\n`);
  process.exit(1);
}
