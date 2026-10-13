// SPDX-License-Identifier: Apache-2.0
//
// CI helper for environments that can read check-run annotations but not workflow logs or
// artifacts. GitHub keeps at most 4096 characters per annotation and 10 annotations of each
// level per step, so output is split accordingly. Commands:
//   run <title> -- <cmd> [args…]          run a command; on failure publish the tail of its output
//                                          as error annotations and exit with the command's status
//   file <path> <title> [--gzip] [--part a/b]
//                                          publish a file (base64) as notice+warning annotations;
//                                          `--part a/b` publishes only the a-th of b slices so large
//                                          files can be spread over several steps
// Annotation data is escaped as required by workflow commands (% CR LF).
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { gzipSync } from 'node:zlib';

const CHUNK = 4000;
const PER_LEVEL = 10;
const ERROR_PARTS = 9;
const ANSI = new RegExp(`${String.fromCharCode(27)}\\[[0-9;]*m`, 'g');

const escapeData = (s) => s.replace(/%/g, '%25').replace(/\r/g, '%0D').replace(/\n/g, '%0A');
const escapeProp = (s) => escapeData(s).replace(/:/g, '%3A').replace(/,/g, '%2C');

function annotate(level, title, message) {
  process.stdout.write(`::${level} title=${escapeProp(title)}::${escapeData(message)}\n`);
}

function chunks(text, size) {
  const out = [];
  for (let i = 0; i < text.length; i += size) out.push(text.slice(i, i + size));
  return out.length > 0 ? out : [''];
}

function run(title, cmd, args) {
  const res = spawnSync(cmd, args, {
    encoding: 'utf8',
    maxBuffer: 256 * 1024 * 1024,
    shell: process.platform === 'win32',
  });
  const output = `${res.stdout ?? ''}${res.stderr ?? ''}`;
  process.stdout.write(output);
  const status = res.status ?? 1;
  if (status !== 0) {
    const plain = output.replace(ANSI, '');
    const tail = plain.slice(-CHUNK * ERROR_PARTS);
    const parts = chunks(tail, CHUNK);
    parts.forEach((p, i) => annotate('error', `${title} failed ${i + 1}/${parts.length}`, p));
  }
  process.exit(status);
}

function file(path, title, gzip, part) {
  const data = readFileSync(path);
  const payload = (gzip ? gzipSync(data, { level: 9 }) : data).toString('base64');
  const all = chunks(payload, CHUNK);
  const [a, b] = part;
  const per = Math.ceil(all.length / b);
  if (per > PER_LEVEL * 2) throw new Error(`${path}: ${all.length} chunks need more than ${b} parts`);
  all.forEach((c, i) => {
    if (Math.floor(i / per) !== a - 1) return;
    const level = i % per < PER_LEVEL ? 'notice' : 'warning';
    annotate(level, `${title} ${gzip ? 'gz' : 'raw'} ${i + 1}/${all.length}`, c);
  });
}

const [command, ...rest] = process.argv.slice(2);
if (command === 'run') {
  const sep = rest.indexOf('--');
  if (sep < 1 || sep === rest.length - 1) throw new Error('usage: run <title> -- <cmd> [args…]');
  run(rest.slice(0, sep).join(' '), rest[sep + 1], rest.slice(sep + 2));
} else if (command === 'file') {
  const [path, title, ...flags] = rest;
  if (!path || !title) throw new Error('usage: file <path> <title> [--gzip] [--part a/b]');
  const partFlag = flags.indexOf('--part');
  const part = partFlag >= 0 ? flags[partFlag + 1].split('/').map(Number) : [1, 1];
  file(path, title, flags.includes('--gzip'), part);
} else {
  throw new Error(`unknown command ${command}`);
}
