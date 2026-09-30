/**
 * The error codes the API returns.
 *
 * The client switches on these rather than on status codes or message text, so
 * wording can change on the server without breaking the app. The list mirrors
 * the Go constants in backend/internal/httpx/response.go; adding a code there
 * means adding it here.
 */
export const ErrorCode = {
  validationFailed: 'validation_failed',
  invalidRequest: 'invalid_request',
  unauthorized: 'unauthorized',
  tokenExpired: 'token_expired',
  tokenInvalid: 'token_invalid',
  forbidden: 'forbidden',
  notFound: 'not_found',
  conflict: 'conflict',
  emailTaken: 'email_taken',
  weakPassword: 'weak_password',
  emailNotVerified: 'email_not_verified',
  invalidLogin: 'invalid_credentials',
  rateLimited: 'rate_limited',
  requestTooLarge: 'request_too_large',
  internalError: 'internal_error',
} as const;

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

/**
 * A failed API call.
 *
 * `fields` carries per-field messages from the server so a form can show every
 * problem at once. The server is careful to report all of them in one response
 * precisely so the user does not have to play whack-a-mole.
 */
export class ApiError extends Error {
  readonly code: ErrorCodeValue | string;
  readonly status: number;
  readonly fields: Record<string, string>;
  /** Present when the server throttled the request, in whole seconds. */
  readonly retryAfterSeconds?: number;
  /** The correlation id from the response, for matching a client report to a log. */
  readonly requestId?: string;

  constructor(init: {
    code: ErrorCodeValue | string;
    message: string;
    status: number;
    fields?: Record<string, string>;
    retryAfterSeconds?: number;
    requestId?: string;
  }) {
    super(init.message);
    this.name = 'ApiError';
    this.code = init.code;
    this.status = init.status;
    this.fields = init.fields ?? {};
    this.retryAfterSeconds = init.retryAfterSeconds;
    this.requestId = init.requestId;
  }

  /** True when retrying the same request later could succeed. */
  get isRetryable(): boolean {
    return (
      this.status >= 500 ||
      this.code === ErrorCode.rateLimited ||
      this.code === ErrorCode.internalError
    );
  }

  /** True when the caller should send the user back to the login screen. */
  get requiresSignIn(): boolean {
    return (
      this.code === ErrorCode.unauthorized ||
      this.code === ErrorCode.tokenExpired ||
      this.code === ErrorCode.tokenInvalid
    );
  }
}

/**
 * The network never reached the API.
 *
 * Kept separate from ApiError so the UI can say "check your connection" rather
 * than "something went wrong", which is the difference between a retry the user
 * attempts and a support ticket they file.
 */
export class NetworkError extends Error {
  constructor(message = 'Could not reach TripVault. Check your connection.') {
    super(message);
    this.name = 'NetworkError';
  }
}

/** The call took too long and was abandoned. */
export class TimeoutError extends Error {
  constructor(ms: number) {
    super(`The request timed out after ${Math.round(ms / 1000)}s.`);
    this.name = 'TimeoutError';
  }
}

/**
 * Narrows an unknown catch value to an ApiError.
 */
export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError;
}

/**
 * A message safe to show a user, for any thrown value.
 */
export function userFacingMessage(err: unknown): string {
  if (err instanceof ApiError || err instanceof NetworkError || err instanceof TimeoutError) {
    return err.message;
  }
  return 'Something went wrong. Please try again.';
}
