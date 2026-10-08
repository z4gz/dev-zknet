// The shell plugin resolves a spawn by the first capability scope whose name
// matches, then builds argv from that scope's argument template: arguments
// beyond the template are dropped silently. Each argv shape therefore needs its
// own scope name, kept in step with the capabilities that allow it.
export const WALLETSHIELD_SCOPE = 'walletshield-listen';
export const WALLETSHIELD_THIN_SCOPE = 'walletshield-listen-thin';

export const walletshieldSpawn = (listenAddress: string, thin: boolean) => {
  const args = ['-listen', listenAddress, '-config', 'client.toml'];
  return thin
    ? { scope: WALLETSHIELD_THIN_SCOPE, args: [...args, '-thin'] }
    : { scope: WALLETSHIELD_SCOPE, args };
};
