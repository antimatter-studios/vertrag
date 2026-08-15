#!/usr/bin/env node
//
// The oracle's reference side for response validation.
//
// Dredd decides pass or fail by handing an expected/actual pair to Gavel. This
// drives the real Gavel over the same pairs vertrag's validator is given, so
// the verdicts can be compared — including the exact error text, which is what
// a failing test run shows the user.
//
//   node validate.js <case.json>
//
// where the case file holds { "expected": {...}, "real": {...} }.

const fs = require('fs');

const gavel = require('gavel');

function main() {
  const [path] = process.argv.slice(2);
  if (!path) {
    throw new Error('expected exactly one case file');
  }

  const { expected, real } = JSON.parse(fs.readFileSync(path, 'utf8'));
  const result = gavel.validate(expected, real);

  // Only the parts Dredd acts on are compared. Gavel also reports the values it
  // was given back to the caller, which is just the input echoed and would make
  // the comparison assert that the harness passed its own arguments through.
  const fields = {};
  Object.keys(result.fields || {}).forEach((name) => {
    const field = result.fields[name];
    fields[name] = {
      valid: field.valid,
      kind: field.kind,
      errors: (field.errors || []).map(error => error.message),
    };
  });

  process.stdout.write(`${JSON.stringify({ valid: result.valid, fields }, null, 2)}\n`);
}

try {
  main();
} catch (err) {
  process.stderr.write(`${err.stack || err.message}\n`);
  process.exit(1);
}
