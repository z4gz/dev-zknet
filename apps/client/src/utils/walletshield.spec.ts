import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { walletshieldSpawn } from './walletshield';

const tauri = join(import.meta.dirname, '../../src-tauri');
const readJson = (file: string) => JSON.parse(readFileSync(join(tauri, file), 'utf8'));

type Arg = string | { validator: string };
type SpawnScope = { name: string; cmd: string; args: Arg[] };
type Permission = string | { identifier: string; allow?: unknown[] };
type Capability = { identifier: string; permissions: Permission[] };

// Without a configured list, a build enables every capability file.
const capabilityFiles: Capability[] = readdirSync(join(tauri, 'capabilities')).map((f) =>
  readJson(join('capabilities', f)),
);
const devLocal = readJson('tauri.dev-local.conf.json');

const enabledCapabilities = (config?: {
  app: { security: { capabilities: (string | Capability)[] } };
}): Capability[] =>
  config
    ? config.app.security.capabilities.map((c) => {
        if (typeof c !== 'string') return c;
        const file = capabilityFiles.find((f) => f.identifier === c);
        if (!file) throw new Error(`capability ${c} not found`);
        return file;
      })
    : capabilityFiles;

const spawnScopes = (capabilities: Capability[]) =>
  capabilities
    .toSorted((a, b) => a.identifier.localeCompare(b.identifier))
    .flatMap((c) => c.permissions)
    .flatMap((p) =>
      typeof p === 'object' && p.identifier === 'shell:allow-spawn'
        ? (p.allow as SpawnScope[])
        : [],
    );

// The argv tauri-plugin-shell 2.x actually executes (`Scope::_prepare`): the
// first scope with a matching name wins, and argv follows its template.
const resolveArgv = (scopes: SpawnScope[], name: string, args: string[]) => {
  const scope = scopes.find((s) => s.name === name);
  if (!scope) throw new Error(`no shell scope named ${name}`);
  return scope.args.map((arg, i) => {
    if (typeof arg === 'string') return arg;
    if (!new RegExp(arg.validator).test(args[i] ?? '')) throw new Error(`argument ${i} rejected`);
    return args[i];
  });
};

describe('walletshield spawn', () => {
  it.each([
    ['production', enabledCapabilities(), false],
    ['dev-local', enabledCapabilities(devLocal), true],
  ])('%s executes exactly the arguments the client passes', (_, capabilities, thin) => {
    const { scope, args } = walletshieldSpawn('127.0.0.1:7070', thin);
    expect(resolveArgv(spawnScopes(capabilities), scope, args)).toEqual(args);
    expect(args.includes('-thin')).toBe(thin);
  });

  it('keeps spawn scope names unique, so no scope shadows another', () => {
    const names = spawnScopes(enabledCapabilities(devLocal)).map((s) => s.name);
    expect(new Set(names).size).toBe(names.length);
  });

  it('adds to every production capability rather than replacing one', () => {
    const ids = enabledCapabilities(devLocal).map((c) => c.identifier);
    expect(ids).toEqual(expect.arrayContaining(capabilityFiles.map((c) => c.identifier)));
  });

  it('keeps the thin scope out of production builds', () => {
    const { scope } = walletshieldSpawn(':7070', true);
    expect(spawnScopes(enabledCapabilities()).some((s) => s.name === scope)).toBe(false);
  });
});
