#!/usr/bin/env node
//
// The oracle's reference side.
//
// This runs the real Dredd over an input and prints what it produces. The Go
// implementation is then run over the same input and the two are compared.
//
// The point of driving Dredd rather than checking in expected output is that
// the expectation stays live: bump the pinned version and any behaviour change
// shows up as a diff instead of silently becoming the new "correct" answer.
//
// Two stages can be driven, because vertrag ports them separately:
//
//   compile.js --media-type <type> [--filename <name>] <api-elements.json>
//       Compile pre-parsed API Elements into transactions.
//
//   compile.js --parse [--filename <name>] <description-document>
//       Parse a description document and compile it, in one pass. This is the
//       end-to-end contract: it is what a format parser has to reproduce.

const fs = require('fs');
const path = require('path');

const fury = require('@apielements/core');
const transactions = require('@antimatter-studios/dredd-transactions');

// The package is published transpiled, so a module's real export may sit behind
// `.default`. Unwrapping here keeps the version pin free to move between a
// CommonJS build and an ES-module one without this script caring.
function callable(value) {
  if (typeof value === 'function') return value;
  if (value && typeof value.default === 'function') return value.default;
  throw new Error('expected a function export from dredd-transactions');
}

const compile = callable(transactions.compile);
const parse = callable(transactions.parse);

function parseArgs(argv) {
  const options = { mediaType: '', filename: '', parse: false };
  const positional = [];

  for (let i = 0; i < argv.length; i += 1) {
    switch (argv[i]) {
      case '--media-type':
        options.mediaType = argv[++i];
        break;
      case '--filename':
        options.filename = argv[++i];
        break;
      case '--parse':
        options.parse = true;
        break;
      default:
        positional.push(argv[i]);
    }
  }

  if (positional.length !== 1) {
    throw new Error('expected exactly one input file');
  }
  [options.path] = positional;
  return options;
}

function emit(result) {
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

// compileElements is the compile stage on its own: API Elements in,
// transactions out. It needs the media type supplied, because that information
// lives in the parse result rather than in the elements themselves.
function compileElements(options) {
  const refract = JSON.parse(fs.readFileSync(options.path, 'utf8'));
  const apiElements = fury.minim.fromRefract(refract);
  emit(compile(options.mediaType, apiElements, options.filename));
}

// parseAndCompile runs the whole front end: a description document in its
// original format, straight through to transactions.
function parseAndCompile(options) {
  const source = fs.readFileSync(options.path, 'utf8');
  const filename = options.filename || path.basename(options.path);

  parse(source, (err, result) => {
    if (err) {
      process.stderr.write(`${err.stack || err.message}\n`);
      process.exit(1);
    }
    emit(compile(result.mediaType, result.apiElements, filename));
  });
}

try {
  const options = parseArgs(process.argv.slice(2));
  if (options.parse) {
    parseAndCompile(options);
  } else {
    compileElements(options);
  }
} catch (err) {
  process.stderr.write(`${err.stack || err.message}\n`);
  process.exit(1);
}
