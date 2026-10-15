#!/usr/bin/env node
// Reproducibility check: build the worker bundle more than once and require
// byte-identical output, i.e. one keccak256 for every build. A specifier pins
// that hash, so anyone rebuilding the pinned commit must be able to arrive at
// it.
//
// Usage:
//   node scripts/reproducible.mjs [--build "<cmd>"] [--bundle <path>] [--runs N]
//                                 [--clean-export [--ref <git-ref>] [--install "<cmd>"]] [--keep]
//
// In place (default): each run deletes the bundle, runs the build command in
//   the workspace, and copies the result aside. Catches nondeterminism inside
//   the build (timestamps, random ids, unordered iteration).
// --clean-export: each run builds a fresh `git archive` of --ref (default
//   HEAD) in its own temporary directory, at paths of different lengths,
//   after the install command. Also catches leaks of the absolute build path
//   and of anything untracked in the working tree. Uncommitted changes are,
//   by design, not part of the export.
//
// Defaults: --build $WORKER_BUILD_CMD or the worker project's Nx build without
// the cache; --bundle $WORKER_BUNDLE or the worker bundle contract path;
// --install "pnpm install --frozen-lockfile --prefer-offline".

import { execFileSync, spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, relative } from 'node:path';
import { parseArgs } from 'node:util';
import { digestFile } from './lib/digest.mjs';
import { DEFAULT_BUNDLE, STATE_DIR, WORKSPACE_DIR } from './lib/paths.mjs';
import { CliError, isMain, runMain } from './lib/proc.mjs';

export const DEFAULT_BUILD = 'pnpm nx run @zknet/anon-rpc-worker:build --skip-nx-cache';
export const DEFAULT_INSTALL = 'pnpm install --frozen-lockfile --prefer-offline';

/** @param {string} cmd @param {string} cwd */
function sh(cmd, cwd) {
  const r = spawnSync('sh', ['-c', cmd], {
    cwd,
    stdio: 'inherit',
    env: { ...process.env, NX_DAEMON: 'false' },
  });
  if (r.status !== 0) throw new CliError(`\`${cmd}\` failed in ${cwd} (exit ${r.status})`);
}

/** @param {Buffer} a @param {Buffer} b @returns {number} first differing offset, -1 if equal */
export function firstDifference(a, b) {
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) if (a[i] !== b[i]) return i;
  return a.length === b.length ? -1 : n;
}

/** @param {string[]} argv */
async function main(argv) {
  const { values } = parseArgs({
    args: argv,
    options: {
      build: { type: 'string', default: process.env.WORKER_BUILD_CMD || DEFAULT_BUILD },
      bundle: { type: 'string', default: process.env.WORKER_BUNDLE || DEFAULT_BUNDLE },
      runs: { type: 'string', default: '2' },
      'clean-export': { type: 'boolean', default: false },
      ref: { type: 'string', default: 'HEAD' },
      install: { type: 'string', default: DEFAULT_INSTALL },
      keep: { type: 'boolean', default: false },
    },
  });
  const runs = Number(values.runs);
  if (!Number.isInteger(runs) || runs < 2) throw new CliError('--runs must be an integer >= 2');
  const build = /** @type {string} */ (values.build);
  const bundleRel = relative(WORKSPACE_DIR, /** @type {string} */ (values.bundle));
  if (bundleRel.startsWith('..'))
    throw new CliError(`bundle ${values.bundle} is outside the workspace`);

  const outDir = join(STATE_DIR, 'reproducible');
  rmSync(outDir, { recursive: true, force: true });
  mkdirSync(outDir, { recursive: true });
  /** @type {string[]} */
  const scratch = [];
  /** @type {import("./lib/digest.mjs").FileDigest[]} */
  const results = [];

  try {
    for (let i = 1; i <= runs; i++) {
      let root = WORKSPACE_DIR;
      if (values['clean-export']) {
        // Different path lengths per run, so an embedded absolute path shows up as a byte difference.
        root = join(mkdtempSync(join(tmpdir(), `zknet-repro-${'x'.repeat(i)}-`)), 'src');
        scratch.push(join(root, '..'));
        mkdirSync(root);
        const archive = execFileSync(
          'git',
          ['archive', '--format=tar', /** @type {string} */ (values.ref)],
          {
            cwd: WORKSPACE_DIR,
            maxBuffer: 1 << 30,
          },
        );
        execFileSync('tar', ['-x', '-C', root], { input: archive });
        console.log(`\n== run ${i}: clean export of ${values.ref} at ${root}`);
        sh(/** @type {string} */ (values.install), root);
      } else {
        console.log(`\n== run ${i}: in place`);
      }
      const bundle = join(root, bundleRel);
      rmSync(bundle, { force: true });
      sh(build, root);
      if (!existsSync(bundle))
        throw new CliError(`run ${i}: the build did not produce ${bundleRel}`);
      const kept = join(outDir, `run-${i}.js`);
      copyFileSync(bundle, kept);
      results.push(await digestFile(kept));
    }
  } finally {
    if (!values.keep) for (const dir of scratch) rmSync(dir, { recursive: true, force: true });
  }

  console.log('');
  for (const r of results)
    console.log(`${r.keccak256}  ${r.bytes} bytes  ${relative(WORKSPACE_DIR, r.file)}`);
  const first = results[0];
  const odd = results.find((r) => r.keccak256 !== first?.keccak256);
  if (first === undefined || odd === undefined) {
    console.log(
      `✓ reproducible: ${runs} builds of ${bundleRel} are byte-identical (keccak256 ${first?.keccak256})`,
    );
    return 0;
  }
  const at = firstDifference(readFileSync(first.file), readFileSync(odd.file));
  console.log(
    `✗ NOT reproducible: ${relative(WORKSPACE_DIR, odd.file)} differs from run 1 at byte ${at}`,
  );
  console.log(`  outputs kept in ${relative(WORKSPACE_DIR, outDir)} for diffing`);
  return 1;
}

if (isMain(import.meta.url)) runMain(main);
