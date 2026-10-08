# ZKN UI Client

```sh
pnpm nx run @zknet/client:desktop-dev         # against the configured remote networks
pnpm nx run @zknet/client:desktop-dev-local   # against the local mixnet; connect to `dev-local`
```

`desktop-dev-local` serves the local mixnet's assets (see `apps/network-assets`) and
runs `tauri dev --config src-tauri/tauri.dev-local.conf.json`. That overlay only exists in
this target. It points `urlNetwork` at the local server, sets `walletshieldThin`, and adds
a `dev-local` capability allowing that server and the thin WalletShield spawn. Packaged
builds contain neither. In thin mode the client re-downloads WalletShield on every
connect, so a rebuilt binary is never shadowed by a cached copy.

Each WalletShield argv shape has its own shell scope name (`src/utils/walletshield.ts`).
The shell plugin runs the first scope with a matching name and builds argv from that
scope's template, so a shared name silently drops extra arguments.
`walletshield.spec.ts` checks this against the capability files.
