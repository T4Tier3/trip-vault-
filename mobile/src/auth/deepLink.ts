/**
 * Parses the links the API puts in transactional email.
 *
 * The API builds links as `<scheme>://<screen>?token=...`, for example
 * `tripvault://reset-password?token=abc`. The scheme must match
 * APP_LINK_SCHEME on the server, or the OS will not route the link to this app.
 */
export type DeepLink =
  | { screen: 'verify-email'; token: string }
  | { screen: 'reset-password'; token: string }
  | { screen: 'unknown'; token: null };

export function parseDeepLink(url: string, scheme: string): DeepLink {
  // A malformed URL should not throw in a link handler, so a bare prefix is
  // enough to pull out what matters.
  const withoutScheme = url.startsWith(`${scheme}://`)
    ? url.slice(scheme.length + 3)
    : null;

  if (withoutScheme === null) {
    return { screen: 'unknown', token: null };
  }

  const [screen, query = ''] = withoutScheme.split('?');
  const token = new URLSearchParams(query).get('token');

  if (screen === 'verify-email' || screen === 'reset-password') {
    // A link with no token cannot do anything, so it is treated as unknown
    // rather than opening a screen that will immediately fail.
    return token ? { screen, token } : { screen: 'unknown', token: null };
  }

  return { screen: 'unknown', token: null };
}
