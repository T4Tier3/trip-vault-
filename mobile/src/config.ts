/**
 * Runtime configuration for the app.
 *
 * The API base URL is baked in at build time rather than fetched at launch,
 * because there is no server to fetch it from and a remote config service would
 * be one more thing to secure. Change it in app.json and rebuild.
 */
export const config = {
  apiBaseUrl: 'http://localhost:8080',
  /** Must match APP_LINK_SCHEME on the API, or emailed links will not open the app. */
  linkScheme: 'tripvault',
  requestTimeoutMs: 15_000,
} as const;
