// The public-tier deploy against a stand-in for Sepolia: a local anvil with
// chain id 11155111, the in-process resolver, and throwaway config, records
// and preflight proofs. Nothing here signs: public-tier sends are only ever
// dry runs or rehearsals on a fork (impersonated), which is what an agent may
// run; a keystore broadcast stays the operator's.

import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, before, test } from 'node:test';
import { digestFile } from '../scripts/lib/digest.mjs';
import { head } from '../scripts/lib/git.mjs';
import { FIXTURE_BUNDLE, PROJECT_DIR } from '../scripts/lib/paths.mjs';
import { foundry, sleep } from '../scripts/lib/proc.mjs';
import { createdRecord, readRecord, writeRecord } from '../scripts/lib/record.mjs';
import { answers } from '../scripts/lib/rpc.mjs';
import { REFERENCE_CODEHASH } from '../scripts/lib/specifier.mjs';
import { createResolverServer, put } from '../scripts/resolver.mjs';

const CHAIN_PORT = 28545;
const FORK_PORT = 28549;
const RPC = `http://127.0.0.1:${CHAIN_PORT}`;
const DEPLOYER = '0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266'; // anvil account 0
const dir = mkdtempSync(join(tmpdir(), 'zknet-deploy-tiers-'));
const store = join(dir, 'store');
const records = join(dir, 'records');
const preflight = join(dir, 'preflight');
const config = join(dir, 'deploy.local.json');
const rehearsal = join(dir, 'rehearse.json');
const second = join(dir, 'second.js');

/** @type {import('node:child_process').ChildProcess} */
let chain;
/** @type {import('node:http').Server} */
let resolver;
let resolverBase = '';

/** @param {string} script @param {string[]} args */
function cli(script, args) {
  return new Promise((resolve) => {
    const p = spawn(process.execPath, [join(PROJECT_DIR, 'scripts', script), ...args], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    p.stdout.on('data', (b) => (out += b));
    p.stderr.on('data', (b) => (out += b));
    p.on('exit', (status) => resolve({ status, out }));
  });
}

/** @param {string} bundle @returns {Promise<string>} the local resolver URL of the bundle */
async function serve(bundle) {
  const d = await put(bundle, store);
  return `${resolverBase}/keccak/${d.keccak256.slice(2, 4)}/${d.keccak256.slice(4)}`;
}

/** @param {string} bundle mark the bundle as proven by the test tier's preflight at HEAD */
async function prove(bundle) {
  const d = await digestFile(bundle);
  mkdirSync(preflight, { recursive: true });
  writeFileSync(
    join(preflight, 'test.json'),
    JSON.stringify({
      tier: 'test',
      workerHash: d.keccak256,
      bytes: d.bytes,
      gitCommit: head(),
      dirty: true,
    }),
  );
  return d.keccak256;
}

/** @param {string} bundle @param {string[]} extra */
const deployTest = (bundle, extra) =>
  cli('deploy.mjs', [
    '--tier',
    'test',
    '--bundle',
    bundle,
    '--config',
    config,
    '--records',
    records,
    '--preflight',
    preflight,
    '--fork-port',
    String(FORK_PORT),
    '--rehearse-record',
    rehearsal,
    ...extra,
  ]);

before(async () => {
  chain = spawn(
    foundry('anvil'),
    ['--quiet', '--port', String(CHAIN_PORT), '--chain-id', '11155111'],
    {
      stdio: 'ignore',
    },
  );
  for (let i = 0; i < 100 && !(await answers(RPC)); i++) await sleep(100);
  resolver = createResolverServer(store);
  await new Promise((r) => resolver.listen(0, '127.0.0.1', () => r(undefined)));
  const address = /** @type {import('node:net').AddressInfo} */ (resolver.address());
  resolverBase = `http://127.0.0.1:${address.port}`;
  writeFileSync(
    config,
    JSON.stringify({
      test: {
        rpcUrl: RPC,
        account: 'unused-in-these-tests',
        deployer: DEPLOYER,
        resolvers: ['https://resolver.invalid/keccak/{path}'],
      },
      prod: {
        rpcUrl: RPC,
        account: 'unused-in-these-tests',
        deployer: DEPLOYER,
        resolvers: ['https://resolver.invalid/keccak/{path}'],
      },
    }),
  );
  copyFileSync(FIXTURE_BUNDLE, second);
  writeFileSync(second, `${readFileSync(second, 'utf8')}\n// second version\n`);
});

after(() => {
  chain?.kill();
  resolver?.close();
});

test('the gate: no preflight proof, or a proof of another bundle, is refused', async () => {
  const none = await deployTest(FIXTURE_BUNDLE, ['--preflight', join(dir, 'nowhere')]);
  assert.equal(none.status, 1);
  assert.match(none.out, /no preflight for test/);
  await prove(second);
  const other = await deployTest(FIXTURE_BUNDLE, []);
  assert.equal(other.status, 1);
  assert.match(other.out, /preflight test proved .* but the bundle is/);
});

test('a public tier refuses a chain other than its own', async () => {
  const d = await digestFile(FIXTURE_BUNDLE);
  writeFileSync(
    join(preflight, 'prod.json'),
    JSON.stringify({ tier: 'prod', workerHash: d.keccak256, gitCommit: head(), dirty: true }),
  );
  const r = await cli('deploy.mjs', [
    '--tier',
    'prod',
    '--bundle',
    FIXTURE_BUNDLE,
    '--config',
    config,
    '--records',
    records,
    '--preflight',
    preflight,
  ]);
  assert.equal(r.status, 1);
  assert.match(r.out, /tier=prod chainId=1/);
  assert.match(r.out, /chain 11155111, but tier prod is chain 1/);
});

test('a public tier signs only as configured, and a broadcast pins only configured resolvers', async () => {
  await prove(FIXTURE_BUNDLE);
  for (const [flags, why] of /** @type {const} */ ([
    [['--password-file', '/dev/null'], /interactive password prompt/],
    [['--account', 'x'], /interactive password prompt/],
    [['--broadcast', '--resolver', 'http://x/y'], /overrides are for dry runs/],
    [['--broadcast', '--skip-resolver-check'], /always checks its resolvers/],
    [['--broadcast', '--rehearse'], /exclusive/],
    [['--deployer', DEPLOYER], /rehearsal stand-in only/],
  ])) {
    const r = await deployTest(FIXTURE_BUNDLE, [...flags]);
    assert.equal(r.status, 1, `${flags.join(' ')}: ${r.out}`);
    assert.match(r.out, why);
  }
});

test('deploy:test is a dry run by default and writes no record', async () => {
  await prove(FIXTURE_BUNDLE);
  const r = await deployTest(FIXTURE_BUNDLE, ['--resolver', await serve(FIXTURE_BUNDLE)]);
  assert.equal(r.status, 0, r.out);
  assert.match(r.out, /tier=test chainId=11155111/);
  assert.match(r.out, /would deploy a new WorkerSpecifier/);
  assert.match(r.out, /nothing sent/);
  assert.match(r.out, /deploy:test --broadcast/);
  assert.equal(readRecord(join(records, 'test.json')), undefined);
});

test('--rehearse deploys on a fork, boot-resolves, and leaves the real chain alone', async () => {
  await prove(FIXTURE_BUNDLE);
  const r = await deployTest(FIXTURE_BUNDLE, [
    '--rehearse',
    '--resolver',
    await serve(FIXTURE_BUNDLE),
  ]);
  assert.equal(r.status, 0, r.out);
  assert.match(r.out, /rehearsal: created 0x[0-9a-fA-F]{40} — gasUsed \d+/);
  assert.match(r.out, /✓ bundle .* hashes to/);
  assert.match(r.out, /✓ rehearsal of deploy:test passed/);
  const would = readRecord(rehearsal);
  assert.equal(would?.chainId, 11155111);
  assert.equal(would?.codehash, REFERENCE_CODEHASH);
  assert.equal(
    readRecord(join(records, 'test.json')),
    undefined,
    'a rehearsal writes no committed record',
  );
  const nonce = await fetch(RPC, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      jsonrpc: '2.0',
      id: 1,
      method: 'eth_getTransactionCount',
      params: [DEPLOYER, 'latest'],
    }),
  }).then((res) => res.json());
  assert.equal(nonce.result, '0x0', 'nothing was sent to the stand-in chain');
});

test('the cycle works on a record whose updates history was removed or emptied', async () => {
  // A live specifier on the stand-in chain, deployed through the local loop.
  const url1 = await serve(FIXTURE_BUNDLE);
  const local = await cli('deploy.mjs', [
    '--bundle',
    FIXTURE_BUNDLE,
    '--rpc-url',
    RPC,
    '--resolver',
    url1,
    '--deployments',
    join(dir, 'local'),
  ]);
  assert.equal(local.status, 0, local.out);
  const address = local.out.trim().split('\n').pop() ?? '';
  const localRecord = JSON.parse(readFileSync(join(dir, 'local', '11155111.json'), 'utf8'));

  const base = createdRecord({
    tier: 'test',
    chainId: 11155111,
    address,
    codehash: REFERENCE_CODEHASH,
    owner: DEPLOYER,
    creation: { tx: localRecord.transactionHash, block: 1 },
    workerHash: localRecord.workerHash,
    bundle: { file: 'fixture', bytes: 1, gitCommit: head(), dirty: true },
  });
  const { updates: _, ...noHistory } = base;
  const url2 = await serve(second);
  for (const curated of [noHistory, { ...base, updates: [] }]) {
    writeRecord(join(records, 'test.json'), /** @type {any} */ (curated));

    await prove(FIXTURE_BUNDLE);
    const same = await deployTest(FIXTURE_BUNDLE, ['--resolver', url1]);
    assert.equal(same.status, 0, same.out);
    assert.match(same.out, /already deployed and up to date/);

    await prove(second);
    const refused = await deployTest(second, ['--resolver', url2]);
    assert.equal(refused.status, 1);
    assert.match(refused.out, /pass --update/);

    const dry = await deployTest(second, ['--update', '--resolver', url2]);
    assert.equal(dry.status, 0, dry.out);
    assert.match(dry.out, /would setWorker/);

    const rehearsed = await deployTest(second, ['--rehearse', '--update', '--resolver', url2]);
    assert.equal(rehearsed.status, 0, rehearsed.out);
    assert.match(rehearsed.out, /rehearsal: updated/);
    const would = readRecord(rehearsal);
    assert.equal(would?.address, address);
    assert.equal(would?.updates?.length, 1);

    const verify = await cli('verify.mjs', [
      '--tier',
      'test',
      '--config',
      config,
      '--records',
      records,
      '--bundle',
      FIXTURE_BUNDLE,
    ]);
    assert.equal(verify.status, 0, verify.out);
    assert.match(verify.out, new RegExp(`specifier ${address}`));
  }
});
