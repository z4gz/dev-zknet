#!/usr/bin/env node
// Boot-resolve a deployed specifier the way a harness does: read
// workerHash() / workerResolvers() through cast, fetch every http(s)
// resolver, and require keccak256(body) == workerHash. With --bundle, also
// require the pinned hash to be that file's.
//
// Usage:
//   node scripts/verify.mjs [--address <addr>] [--rpc-url <url>] [--bundle <path>] [--deployments <dir>]
//
// Without --address, the address comes from <deployments>/<chainId>.json.
// Exits 1 unless at least one resolver admits the bundle and none serves
// other bytes.

import { parseArgs } from 'node:util';
import { digestFile } from './lib/digest.mjs';
import { DEFAULT_RPC_URL, DEPLOYMENTS_DIR } from './lib/paths.mjs';
import { CliError, isMain, runMain } from './lib/proc.mjs';
import { chainId } from './lib/rpc.mjs';
import { checkResolver, readSpecifier, REFERENCE_CODEHASH } from './lib/specifier.mjs';
import { readDeployment } from './deploy.mjs';

/** @param {string[]} argv */
async function main(argv) {
  const { values } = parseArgs({
    args: argv,
    options: {
      address: { type: 'string' },
      'rpc-url': { type: 'string', default: process.env.RPC_URL || DEFAULT_RPC_URL },
      bundle: { type: 'string' },
      deployments: { type: 'string', default: DEPLOYMENTS_DIR },
    },
  });
  const rpcUrl = /** @type {string} */ (values['rpc-url']);
  const id = await chainId(rpcUrl);
  const address =
    values.address ?? readDeployment(/** @type {string} */ (values.deployments), id)?.address;
  if (!address) throw new CliError(`no --address and no recorded deployment for chain ${id}`);

  const spec = await readSpecifier(rpcUrl, address);
  if (!spec) throw new CliError(`no code at ${address} on chain ${id}`);
  console.log(`specifier ${address} on chain ${id}`);
  console.log(
    `  codehash        ${spec.codehash}${spec.codehash === REFERENCE_CODEHASH ? '  (EF reference WorkerSpecifier)' : '  (NOT the reference)'}`,
  );
  console.log(`  owner           ${spec.owner}`);
  console.log(`  workerHash()    ${spec.workerHash}`);
  spec.resolvers.forEach((r, i) => console.log(`  workerResolvers()[${i}] ${r}`));

  let failures = 0;
  if (values.bundle) {
    const digest = await digestFile(values.bundle);
    const same = digest.keccak256 === spec.workerHash;
    console.log(`${same ? '✓' : '✗'} bundle ${values.bundle} hashes to ${digest.keccak256}`);
    if (!same) failures++;
  }

  let admitted = 0;
  for (const r of spec.resolvers) {
    const check = await checkResolver(r, spec.workerHash);
    const fetched = /^https?:\/\//.test(r);
    console.log(`${check.ok ? (fetched ? '✓' : '·') : '✗'} ${r}: ${check.detail}`);
    if (!check.ok) failures++;
    else if (fetched) admitted++;
  }
  if (admitted === 0) {
    console.log('✗ no resolver served bytes matching workerHash()');
    failures++;
  }
  return failures === 0 ? 0 : 1;
}

if (isMain(import.meta.url)) runMain(main);
