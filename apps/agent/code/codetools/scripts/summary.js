// Compacts a mocha, jest/vitest or eslint JSON report into the summary
// codetools parses. Usage: node -e <script> <kind> <report> <output>
const fs = require('fs');
const path = require('path');

const [kind, reportFile, outputFile] = process.argv.slice(1);
const cwd = process.cwd();
const MAX_FAILURES = 30;
const MAX_DIAGNOSTICS = 200;

const read = (f) => { try { return fs.readFileSync(f, 'utf8'); } catch { return ''; } };
const clip = (s, n) => { s = String(s || ''); return s.length > n ? s.slice(0, n) + '...' : s; };
const tail = (s) => (s.length > 4000 ? '...' + s.slice(-4000) : s);
const rel = (f) => (f && path.isAbsolute(f) ? path.relative(cwd, f) : f || '');
const ansi = /\x1b\[[0-9;]*m/g;

// splitStack separates "message\n    at frame" text.
function splitStack(text) {
  const lines = String(text || '').replace(ansi, '').split('\n');
  const i = lines.findIndex((l) => /^\s+at /.test(l));
  if (i < 0) return { message: clip(lines.join('\n'), 1500), stack: '' };
  return { message: clip(lines.slice(0, i).join('\n'), 1500), stack: clip(lines.slice(i).join('\n'), 1500) };
}

const out = { passed: 0, failed: 0, skipped: 0, cwd, failures: [], output: '' };
const addFailure = (f) => { if (out.failures.length < MAX_FAILURES) out.failures.push(f); };

// mocha --reporter json writes to stdout, so console output from the tests
// can surround the report.
function mocha(text) {
  const start = text.indexOf('{\n  "stats"');
  const end = text.lastIndexOf('\n}');
  if (start < 0 || end < start) throw new Error('no mocha JSON report in the output');
  out.output = text.slice(0, start) + text.slice(end + 2);
  const r = JSON.parse(text.slice(start, end + 2));
  out.passed = r.stats.passes || 0;
  out.failed = r.stats.failures || 0;
  out.skipped = r.stats.pending || 0;
  for (const f of r.failures || []) {
    const err = f.err || {};
    addFailure({ file: rel(f.file), test: f.fullTitle || f.title || '', message: clip(err.message, 1500), stack: splitStack(err.stack).stack });
  }
}

// jest --json and vitest --reporter=json share this format. A suite that
// fails to load has no assertions; it counts as one failure.
function jest(text) {
  const r = JSON.parse(text);
  out.passed = r.numPassedTests || 0;
  out.failed = r.numFailedTests || 0;
  out.skipped = (r.numPendingTests || 0) + (r.numTodoTests || 0);
  for (const s of r.testResults || []) {
    const file = rel(s.name || s.testFilePath);
    let failedAssertions = 0;
    for (const a of s.assertionResults || []) {
      if (a.status !== 'failed') continue;
      failedAssertions++;
      addFailure({ file, test: a.fullName || a.title || '', ...splitStack((a.failureMessages || []).join('\n')) });
    }
    if (failedAssertions === 0 && s.status === 'failed' && s.message) {
      out.failed++;
      addFailure({ file, test: '', ...splitStack(s.message) });
    }
  }
}

function eslint(text) {
  out.diagnostics = [];
  for (const f of JSON.parse(text)) {
    for (const m of f.messages || []) {
      if (out.diagnostics.length >= MAX_DIAGNOSTICS) return;
      out.diagnostics.push({
        file: rel(f.filePath), line: m.line || 0, column: m.column || 0,
        severity: m.severity === 2 || m.fatal ? 'error' : 'warning', source: 'eslint',
        message: m.ruleId ? `${m.message} (${m.ruleId})` : m.message,
      });
    }
  }
}

try {
  const report = read(reportFile);
  if (!report.trim()) throw new Error(`the ${kind} report was not written`);
  ({ mocha, jest, vitest: jest, eslint })[kind](report);
} catch (e) {
  out.error = e.message;
}
out.output = tail((out.output + '\n' + read(outputFile)).trim());
console.log(JSON.stringify(out));
