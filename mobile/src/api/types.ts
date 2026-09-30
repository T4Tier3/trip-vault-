/**
 * The shapes the API returns.
 *
 * These mirror the Go response structs. Keeping them in one file makes the
 * contract reviewable in one place, and a backend change that renames a field
 * becomes a type error here rather than `undefined` at runtime.
 */

export interface TokenPair {
  accessToken: string;
  refreshToken: string;
  tokenType: string;
  /** RFC 3339 timestamp, used to refresh slightly before the token actually dies. */
  expiresAt: string;
  expiresIn: number;
}

export interface User {
  id: string;
  email: string;
  name: string;
  avatarUrl?: string;
  emailVerified: boolean;
  createdAt: string;
}

export interface AuthResponse {
  tokens: TokenPair;
  user: User;
}

export interface MessageResponse {
  message: string;
}

/**
 * Registration does not sign in, so there is no token pair here.
 *
 * needsVerification is what the app uses to decide whether to send the user to
 * the confirmation screen, rather than assuming it from the fact that they just
 * registered.
 */
export interface RegisterResponse {
  userId: string;
  email: string;
  name: string;
  needsVerification: boolean;
  message: string;
}

export interface ProfileResponse {
  id: string;
  email: string;
  name: string;
  avatarUrl?: string;
  emailVerified: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface Envelope<T> {
  data: T;
  meta?: Record<string, unknown>;
}

export interface ErrorEnvelope {
  error: {
    code: string;
    message: string;
    fields?: Record<string, string>;
    requestId?: string;
  };
}
