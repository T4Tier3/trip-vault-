/**
 * Runtime configuration for the app.
 *
 * The API base URL is read from the environment at build time rather than
 * fetched at launch, because there is no server to fetch it from and a remote
 * config service would be one more thing to secure.
 *
 * `EXPO_PUBLIC_` variables are inlined by Metro when the bundle is built, so
 * setting one in `mobile/.env` is enough; no rebuild step is needed, Metro picks
 * it up on reload.
 *
 * The default suits a simulator, where the API really is on the same machine. It
 * does not suit a physical device: `localhost` there is the phone itself, so the
 * request goes nowhere and the app reports that it cannot reach TripVault. Set
 * `EXPO_PUBLIC_API_BASE_URL` to this machine's LAN address instead. See
 * `mobile/.env.example`.
 */
const DEFAULT_API_BASE_URL = 'http://localhost:8080';

export const config = {
  apiBaseUrl: process.env.EXPO_PUBLIC_API_BASE_URL ?? DEFAULT_API_BASE_URL,
  /** Must match APP_LINK_SCHEME on the API, or emailed links will not open the app. */
  linkScheme: 'tripvault',
  requestTimeoutMs: 15_000,
} as const;
