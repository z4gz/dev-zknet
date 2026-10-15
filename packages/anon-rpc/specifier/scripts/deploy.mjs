#!/usr/bin/env node
// Deploy a WorkerSpecifier pinning a bundle's keccak256, or confirm the one
// already recorded for this chain. Idempotent: re-running with the same
// bundle and resolvers is a no-op that prints the same address.
//
// Usage:
//   node scripts/deploy.mjs [--bundle <path>] [--rpc-url <url>] [--resolver <entry>]...
//                           [--from <addr>] [--account <name> | --keystore <path>] [--password-file <path>]
//                           [--deployments <dir>] [--update] [--fresh] [--dry-run] [--skip-resolver-check]
//
// Every chain-facing value is an input, so a public testnet is configuration:
//   RPC_URL / --rpc-url        default http://127.0.0.1:8545 (local anvil)
//   RESOLVER_URLS / --resolver comma-separated / repeatable; default: the local
//                              resolver URL of the bundle
//   signer                     default: the chain's first unlocked account
//                              (anvil); --account/--keystore select a Foundry
//                              keystore instead, unlocked with --password-file.
//                              This script never reads or prints key material.
//
// The record of what was deployed is <deployments>/<chainId>.json (default
// .state/deployments). With a record whose code still exists:
//   same hash and resolvers  -> nothing to do
//   different                -> refuses, unless --update (owner setWorker on
//                               the same address) or --fresh (new contract)
// Resolvers must serve the pinned bytes before anything is sent (a harness
// would reject them anyway), unless --skip-resolver-check.

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { parseArgs } from 'node:util';
import { digestFile } from './lib/digest.mjs';
import {
  DEFAULT_BUNDLE,
  DEFAULT_RESOLVER_PORT,
  DEFAULT_RPC_URL,
  DEPLOYMENTS_DIR,
} from './lib/paths.mjs';
import { CliError, foundry, isMain, run, runMain } from './lib/proc.mjs';
import { chainId as getChainId, rpc } from './lib/rpc.mjs';
import { checkResolver, readSpecifier, REFERENCE_CODEHASH, sameList } from './lib/specifier.mjs';
import { resolverUrl } from './resolver.mjs';

/**
 * @typedef {object} Deployment
 * @property {number} chainId
 * @property {string} address
 * @property {string} transactionHash  the creation transaction
 * @property {string} deployer
 * @property {string} workerHash
 * @property {string[]} resolvers
 * @property {string} bundle            file name the hash was taken from (informational)
 */

/** @param {string} dir @param {number} id @returns {Deployment | undefined} */
export function readDeployment(dir, id) {
  const file = join(dir, `${id}.json`);
  return existsSync(file) ? JSON.parse(readFileSync(file, 'utf8')) : undefined;
}

/** @param {string} dir @param {Deployment} record */
function writeDeployment(dir, record) {
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, `${record.chainId}.json`), `${JSON.stringify(record, null, 2)}\n`);
}

/** @param {string[]} argv */
async function main(argv) {
  const { values } = parseArgs({
    args: argv,
    options: {
      bundle: { type: 'string', default: process.env.WORKER_BUNDLE || DEFAULT_BUNDLE },
      'rpc-url': { type: 'string', default: process.env.RPC_URL || DEFAULT_RPC_URL },
      resolver: { type: 'string', multiple: true, default: [] },
      from: { type: 'string', default: process.env.ETH_FROM },
      account: { type: 'string' },
      keystore: { type: 'string' },
      'password-file': { type: 'string' },
      deployments: { type: 'string', default: DEPLOYMENTS_DIR },
      update: { type: 'boolean', default: false },
      fresh: { type: 'boolean', default: false },
      'dry-run': { type: 'boolean', default: false },
      'skip-resolver-check': { type: 'boolean', default: false },
    },
  });
  const rpcUrl = /** @type {string} */ (values['rpc-url']);
  const bundle = /** @type {string} */ (values.bundle);
  const dir = /** @type {string} */ (values.deployments);
  if (!existsSync(bundle))
    throw new CliError(`bundle not found: ${bundle} (build the worker, or pass --bundle)`);
  if (values.update && values.fresh) throw new CliError('--update and --fresh are exclusive');

  const digest = await digestFile(bundle);
  const workerHash = digest.keccak256;
  const fromEnv = (process.env.RESOLVER_URLS ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  const given = /** @type {string[]} */ (values.resolver);
  const resolvers =
    given.length > 0
      ? given
      : fromEnv.length > 0
        ? fromEnv
        : [resolverUrl('127.0.0.1', DEFAULT_RESOLVER_PORT, workerHash)];

  // Signer: a keystore when named, else an unlocked account of the node.
  /** @type {string[]} */
  let signer;
  let deployer;
  if (values.account || values.keystore) {
    signer = values.account
      ? ['--account', values.account]
      : ['--keystore', /** @type {string} */ (values.keystore)];
    if (values['password-file']) signer.push('--password-file', values['password-file']);
    deployer = await run(foundry('cast'), ['wallet', 'address', ...signer]);
  } else {
    const accounts = /** @type {string[]} */ (await rpc(rpcUrl, 'eth_accounts'));
    deployer = values.from ?? accounts[0];
    if (!deployer)
      throw new CliError(
        `${rpcUrl} has no unlocked account; name a keystore with --account or --keystore`,
      );
    signer = ['--unlocked', '--from', deployer];
  }

  const chainId = await getChainId(rpcUrl);
  console.log(`chain ${chainId} at ${rpcUrl}`);
  console.log(`bundle ${bundle} (${digest.bytes} bytes)`);
  console.log(`workerHash ${workerHash}`);
  for (const r of resolvers) console.log(`resolver ${r}`);
  console.log(`signer ${deployer}`);

  if (!values['skip-resolver-check']) {
    for (const r of resolvers) {
      const check = await checkResolver(r, workerHash);
      if (!check.ok)
        throw new CliError(
          `resolver ${r}: ${check.detail} (start the resolver, or --skip-resolver-check)`,
        );
      console.log(`✓ resolver serves the pinned bytes: ${r}`);
    }
  }

  const record = values.fresh ? undefined : readDeployment(dir, chainId);
  const current = record ? await readSpecifier(rpcUrl, record.address) : undefined;
  if (current && current.codehash !== REFERENCE_CODEHASH) {
    throw new CliError(
      `recorded address ${record?.address} holds other code (codehash ${current.codehash}); use --fresh`,
    );
  }

  if (
    record &&
    current &&
    current.workerHash === workerHash &&
    sameList(current.resolvers, resolvers)
  ) {
    console.log(`✓ already deployed and up to date: ${record.address}`);
    console.log(record.address);
    return;
  }

  const send = async (/** @type {string} */ what, /** @type {() => Promise<string>} */ act) => {
    if (values['dry-run']) {
      console.log(`dry run: stopping before ${what}`);
      return undefined;
    }
    console.log(`${what}…`);
    return act();
  };

  let address;
  let transactionHash;
  if (record && current) {
    if (!values.update) {
      throw new CliError(
        `specifier ${record.address} pins ${current.workerHash} with ${current.resolvers.length} resolver(s); ` +
          'pass --update to setWorker on it, or --fresh to deploy a new one',
      );
    }
    if (current.owner.toLowerCase() !== deployer.toLowerCase()) {
      throw new CliError(
        `specifier ${record.address} is owned by ${current.owner}, not the signer ${deployer}`,
      );
    }
    const out = await send(`setWorker on ${record.address}`, () =>
      run(foundry('cast'), [
        'send',
        '--json',
        '--rpc-url',
        rpcUrl,
        ...signer,
        record.address,
        'setWorker(bytes32,string[])',
        workerHash,
        JSON.stringify(resolvers),
      ]),
    );
    if (out === undefined) return;
    if (JSON.parse(out).status !== '0x1') throw new CliError(`setWorker reverted: ${out}`);
    address = record.address;
    transactionHash = record.transactionHash;
  } else {
    if (record)
      console.log(
        `recorded specifier ${record.address} has no code on this chain (chain reset?); deploying anew`,
      );
    const out = await send('deploying WorkerSpecifier', () =>
      run(foundry('forge'), [
        'create',
        'src/WorkerSpecifier.sol:WorkerSpecifier',
        '--rpc-url',
        rpcUrl,
        ...signer,
        '--broadcast',
        '--json',
        '--constructor-args',
        workerHash,
        JSON.stringify(resolvers),
      ]),
    );
    if (out === undefined) return;
    const created = JSON.parse(out.slice(out.indexOf('{')));
    address = created.deployedTo;
    transactionHash = created.transactionHash;
  }

  const after = await readSpecifier(rpcUrl, address);
  if (!after) throw new CliError(`no code at ${address} after the transaction`);
  if (after.codehash !== REFERENCE_CODEHASH)
    throw new CliError(`deployed code differs from the reference (codehash ${after.codehash})`);
  if (after.workerHash !== workerHash)
    throw new CliError(`read-back workerHash() ${after.workerHash}, expected ${workerHash}`);
  if (!sameList(after.resolvers, resolvers))
    throw new CliError(`read-back workerResolvers() ${JSON.stringify(after.resolvers)}`);
  console.log('✓ on-chain read-back matches');

  writeDeployment(dir, {
    chainId,
    address,
    transactionHash,
    deployer,
    workerHash,
    resolvers,
    bundle: bundle.split('/').pop() ?? bundle,
  });
  console.log(address);
}

if (isMain(import.meta.url)) runMain(main);
