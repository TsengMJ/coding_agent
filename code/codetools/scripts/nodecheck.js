// Syntax-checks JavaScript files and prints the summary codetools parses.
// CommonJS files compile in memory; ES modules and files that fail as
// CommonJS go through "node --check", which knows the module type.
// Usage: node -e <script> [path...]
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const Module = require('module');
const { spawnSync } = require('child_process');

const SKIP_DIRS = new Set(['.git', '.hg', '.svn', 'node_modules', 'bower_components', 'vendor', 'dist', 'build', 'coverage', 'out', '.next', '.nuxt', '.cache']);
const MAX_DIAGNOSTICS = 200;
const MAX_FILES = 2000;
const JS = /\.(js|cjs|mjs)$/;
const TS = /\.(ts|tsx|mts|cts)$/;

const diags = [];
let checked = 0;
let capped = false;
let typescript = 0;

function* walk(p) {
  let st;
  try { st = fs.statSync(p); } catch { return; }
  if (st.isFile()) { yield path.normalize(p); return; }
  if (!st.isDirectory()) return;
  for (const e of fs.readdirSync(p, { withFileTypes: true })) {
    if (e.isDirectory() && SKIP_DIRS.has(e.name)) continue;
    yield* walk(path.join(p, e.name));
  }
}

// cliError parses "file:line\n<source>\n   ^\n\nSyntaxError: message".
function cliError(file, stderr) {
  const lines = stderr.split('\n');
  const loc = lines[0].match(/:(\d+)$/);
  const msg = lines.find((l) => /^\w*Error: /.test(l)) || lines.filter(Boolean).pop() || 'syntax check failed';
  return { file, line: loc ? Number(loc[1]) : 0, column: 0, severity: 'error', source: 'node', message: msg.trim() };
}

function check(file) {
  if (!file.endsWith('.mjs')) {
    try {
      const src = fs.readFileSync(file, 'utf8').replace(/^#!.*/, '');
      new vm.Script(Module.wrap(src), { filename: file });
      return null;
    } catch (e) {
      if (!(e instanceof SyntaxError)) return { file, line: 0, column: 0, severity: 'error', source: 'node', message: e.message };
    }
  }
  const r = spawnSync(process.execPath, ['--check', file], { encoding: 'utf8', timeout: 30000 });
  if (r.status === 0) return null;
  return cliError(file, r.stderr || (r.error && r.error.message) || '');
}

for (const root of process.argv.slice(1).length ? process.argv.slice(1) : ['.']) {
  for (const file of walk(root)) {
    if (TS.test(file) && !file.endsWith('.d.ts')) { typescript++; continue; }
    if (!JS.test(file) || file.endsWith('.min.js')) continue;
    if (checked >= MAX_FILES || diags.length >= MAX_DIAGNOSTICS) { capped = true; continue; }
    checked++;
    const d = check(file);
    if (d) diags.push(d);
  }
}
if (capped) diags.push({ file: '', line: 0, column: 0, severity: 'info', source: 'node', message: `only the first ${checked} JavaScript files were checked; pass files to check others` });
if (typescript) diags.push({ file: '', line: 0, column: 0, severity: 'info', source: 'node', message: `no tsconfig.json: ${typescript} TypeScript files were not type-checked` });
console.log(JSON.stringify({ diagnostics: diags }));
