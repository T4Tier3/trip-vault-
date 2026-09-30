import { ApiError, NetworkError, TimeoutError, ErrorCode } from './errors';
import type { TokenStore } from '../auth/tokenStore';
import type {
  AuthResponse,
  Envelope,
  ErrorEnvelope,
  MessageResponse,
  ProfileResponse,
  RegisterResponse,
  TokenPair,
  User,
} from './types';

/** The subset of fetch the client uses, so a test can supply its own. */
export type FetchLike = (
  input: string,
  init?: {
    method?: string;
    headers?: Record<string, string>;
    body?: string;
    signal?: AbortSignal;
  },
) => Promise<FetchLikeResponse>;

export interface FetchLikeResponse {
  ok: boolean;
  status: number;
  headers: { get(name: string): string | null };
  text(): Promise<string>;
}

export interface ApiClientOptions {
  baseUrl: string;
  store: TokenStore;
  fetchImpl?: FetchLike;
  /** How long a single request may take before it is abandoned. */
  timeoutMs?: number;
  /**
   * Called when the session is gone for good, so the UI can show the login
   * screen. Invoked at most once per failed refresh.
   */
  onSignedOut?: () => void;
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  body?: unknown;
  /** Set for the endpoints that must not carry a token, such as login. */
  anonymous?: boolean;
  signal?: AbortSignal;
}

const DEFAULT_TIMEOUT_MS = 15_000;

/**
 * The HTTP client, including access-token handling.
 *
 * The interesting part is refresh: access tokens are short lived, so requests
 * will start failing with 401 purely because time passed. On a 401 the client
 * refreshes once and replays the request.
 *
 * Concurrent requests are the trap. If five screens load at once and the token
 * has expired, all five get a 401 at the same moment. Without coordination all
 * five would call /auth/refresh with the same refresh token, and because the
 * server rotates refresh tokens and treats reuse as theft, the first would win
 * and the rest would be rejected as a stolen token, signing the user out. So
 * refresh is single-flight: one request in flight, everyone else waits for it.
 */
export class ApiClient {
  private readonly baseUrl: string;
  private readonly store: TokenStore;
  private readonly fetchImpl: FetchLike;
  private readonly timeoutMs: number;
  private readonly onSignedOut: (() => void) | undefined;

  /** The in-flight refresh, if any. Shared by every caller that needs one. */
  private refreshing: Promise<TokenPair> | null = null;

  constructor(options: ApiClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.store = options.store;
    this.fetchImpl = options.fetchImpl ?? (globalThis.fetch as unknown as FetchLike);
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.onSignedOut = options.onSignedOut;

    if (!this.fetchImpl) {
      throw new Error('ApiClient needs a fetch implementation');
    }
  }

  /**
   * Sends a request, refreshing the access token once if the server says it is
   * no longer valid.
   */
  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const { method = 'GET', body, anonymous = false, signal } = options;

    if (anonymous) {
      return this.send<T>(path, method, body, null, signal);
    }

    const tokens = await this.store.read();
    if (!tokens) {
      throw new ApiError({
        code: ErrorCode.unauthorized,
        message: 'Please sign in to continue.',
        status: 401,
      });
    }

    try {
      return await this.send<T>(path, method, body, tokens.accessToken, signal);
    } catch (err) {
      // Only an expired or invalid token is worth retrying. A 403 or a
      // validation failure would fail identically the second time, and retrying
      // would double the side effects of a request that may have partly run.
      if (!(err instanceof ApiError) || !err.requiresSignIn) {
        throw err;
      }

      const fresh = await this.refresh();
      return this.send<T>(path, method, body, fresh.accessToken, signal);
    }
  }

  /**
   * Exchanges the refresh token for a new pair, at most once at a time.
   */
  private refresh(): Promise<TokenPair> {
    // Reuse the call already running rather than starting a second one. This one
    // line is what stops a burst of 401s from logging the user out.
    if (this.refreshing) {
      return this.refreshing;
    }

    const attempt = (async (): Promise<TokenPair> => {
      const current = await this.store.read();
      if (!current) {
        throw new ApiError({
          code: ErrorCode.unauthorized,
          message: 'Please sign in to continue.',
          status: 401,
        });
      }

      try {
        // The refresh call itself must never recurse into refresh(). The endpoint
        // answers with the same shape as login, so the pair sits under "tokens".
        const result = await this.send<{ tokens?: TokenPair }>(
          '/auth/refresh',
          'POST',
          { refreshToken: current.refreshToken },
          null,
        );

        const pair = result?.tokens;
        if (!pair?.accessToken || !pair?.refreshToken) {
          // A 200 with no tokens means the server and client disagree about the
          // contract. Treating the session as dead is safer than looping.
          await this.store.clear();
          this.onSignedOut?.();
          throw new ApiError({
            code: ErrorCode.tokenInvalid,
            message: 'Your session could not be renewed. Please sign in again.',
            status: 401,
          });
        }

        await this.store.write(pair);
        return pair;
      } catch (err) {
        // The refresh token is spent, revoked or rejected. There is nothing left
        // to recover, so clear the session and tell the app.
        await this.store.clear();
        this.onSignedOut?.();
        throw err;
      } finally {
        // Clear before anyone awaiting this promise resumes, so a later request
        // starts a fresh attempt rather than reusing a settled one.
        this.refreshing = null;
      }
    })();

    this.refreshing = attempt;
    return attempt;
  }

  /**
   * Signs the user out, telling the server to revoke the session when possible.
   *
   * A failure here is not reported to the user: the local session is cleared
   * regardless, and leaving a token on the device after someone taps "sign out"
   * would be worse than an unrevoked server-side record that expires on its own.
   */
  async logout(): Promise<void> {
    const tokens = await this.store.read();
    await this.store.clear();

    if (!tokens) {
      return;
    }
    try {
      await this.send('/auth/logout', 'POST', { refreshToken: tokens.refreshToken }, null);
    } catch {
      // Deliberately swallowed; see above.
    }
  }

  /** Performs one HTTP round trip and maps the result. */
  private async send<T>(
    path: string,
    method: string,
    body: unknown,
    accessToken: string | null,
    signal?: AbortSignal,
  ): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    if (accessToken) {
      headers.Authorization = `Bearer ${accessToken}`;
    }

    // The caller cancelled before the request went out, most often because the
    // screen went away. Returning now is better than starting a request whose
    // result nobody will read.
    if (signal?.aborted) {
      throw new ApiError({ code: 'aborted', message: 'Request cancelled.', status: 0 });
    }

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    // A caller-supplied signal must also be able to cancel the request.
    const onExternalAbort = () => controller.abort();
    signal?.addEventListener('abort', onExternalAbort);

    let response: FetchLikeResponse;
    try {
      response = await this.fetchImpl(`${this.baseUrl}${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: controller.signal,
      });
    } catch (err) {
      // An abort is either our timeout or the caller's cancellation, and the
      // two deserve different messages.
      if (controller.signal.aborted) {
        throw signal?.aborted
          ? new ApiError({ code: 'aborted', message: 'Request cancelled.', status: 0 })
          : new TimeoutError(this.timeoutMs);
      }
      throw new NetworkError();
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onExternalAbort);
    }

    const raw = await response.text();

    if (!response.ok) {
      throw this.toApiError(response, raw);
    }

    if (raw === '') {
      return undefined as T;
    }

    try {
      const parsed = JSON.parse(raw) as Envelope<T> | T;
      // Unwrap the envelope, but tolerate a bare payload so a future endpoint
      // that forgets the wrapper does not break the app.
      if (parsed && typeof parsed === 'object' && 'data' in parsed) {
        return (parsed as Envelope<T>).data;
      }
      return parsed as T;
    } catch {
      throw new ApiError({
        code: ErrorCode.internalError,
        message: 'The server sent a response the app could not read.',
        status: response.status,
      });
    }
  }

  /** Turns a non-2xx response into an ApiError. */
  private toApiError(response: FetchLikeResponse, raw: string): ApiError {
    const retryAfterHeader = response.headers.get('Retry-After');
    const retryAfterSeconds = retryAfterHeader ? Number.parseInt(retryAfterHeader, 10) : undefined;

    let parsed: ErrorEnvelope | undefined;
    try {
      const candidate = JSON.parse(raw) as ErrorEnvelope;
      if (candidate?.error?.code) {
        parsed = candidate;
      }
    } catch {
      // Not JSON, which happens when a proxy or load balancer answers instead
      // of the app. Fall through to a generic error below.
    }

    if (!parsed) {
      return new ApiError({
        code: response.status >= 500 ? ErrorCode.internalError : ErrorCode.invalidRequest,
        message:
          response.status >= 500
            ? 'TripVault is having trouble. Please try again shortly.'
            : 'That request could not be completed.',
        status: response.status,
        ...(retryAfterSeconds !== undefined ? { retryAfterSeconds } : {}),
      });
    }

    return new ApiError({
      code: parsed.error.code,
      message: parsed.error.message,
      status: response.status,
      fields: parsed.error.fields,
      ...(parsed.error.requestId ? { requestId: parsed.error.requestId } : {}),
      ...(retryAfterSeconds !== undefined ? { retryAfterSeconds } : {}),
    });
  }

  // ---------------------------------------------------------------------------
  // Endpoint helpers
  // ---------------------------------------------------------------------------

  register(input: { email: string; password: string; name: string }): Promise<RegisterResponse> {
    return this.request('/auth/register', { method: 'POST', body: input, anonymous: true });
  }

  login(input: {
    email: string;
    password: string;
    deviceId?: string;
  }): Promise<AuthResponse> {
    return this.request('/auth/login', { method: 'POST', body: input, anonymous: true });
  }

  // The token goes in the body, not the query string. The API accepts either,
  // but a query string lands in access logs and proxy logs, and the rule this
  // codebase follows is that a token is never written to a log.
  verifyEmail(token: string): Promise<MessageResponse> {
    return this.request('/auth/verify-email', {
      method: 'POST',
      body: { token },
      anonymous: true,
    });
  }

  resendVerification(email: string): Promise<MessageResponse> {
    return this.request('/auth/resend-verification', {
      method: 'POST',
      body: { email },
      anonymous: true,
    });
  }

  forgotPassword(email: string): Promise<MessageResponse> {
    return this.request('/auth/forgot-password', {
      method: 'POST',
      body: { email },
      anonymous: true,
    });
  }

  resetPassword(token: string, password: string): Promise<MessageResponse> {
    return this.request('/auth/reset-password', {
      method: 'POST',
      body: { token, password },
      anonymous: true,
    });
  }

  me(): Promise<ProfileResponse> {
    return this.request('/users/me');
  }

  updateProfile(input: {
    name?: string;
    avatarUrl?: string | null;
  }): Promise<ProfileResponse> {
    // Only the fields the caller actually set are sent, so an omitted field is
    // left alone by the server rather than being cleared.
    const body: Record<string, unknown> = {};
    if (input.name !== undefined) {
      body.name = input.name;
    }
    if (input.avatarUrl !== undefined) {
      body.avatarUrl = input.avatarUrl;
    }
    return this.request('/users/me', { method: 'PATCH', body });
  }

  /** The signed-in user, for screens that need the current profile. */
  async currentUser(): Promise<User> {
    return this.me();
  }
}
