import { ApiClient } from '../api/client';
import { ApiError, ErrorCode } from '../api/errors';
import type { AuthResponse, User } from '../api/types';
import type { TokenStore } from './tokenStore';

/**
 * Where the app is in the sign-in journey.
 *
 * Modelled as a small state machine rather than a pile of booleans, because the
 * states are genuinely exclusive: a user is either anonymous, unverified, or
 * signed in, and the transitions between them are the rules worth being able to
 * state in one place.
 */
export type AuthState =
  | { status: 'loading' }
  | { status: 'anonymous' }
  /** Has an account but has not confirmed the email address yet. */
  | { status: 'unverified'; user: User }
  | { status: 'authenticated'; user: User }
  | { status: 'error'; message: string };

/**
 * Owns the session and the calls that change it.
 *
 * Deliberately free of React so the rules can be tested directly, and so the
 * provider stays a thin wrapper.
 */
export class Session {
  private state: AuthState = { status: 'loading' };
  private readonly listeners = new Set<(state: AuthState) => void>();

  constructor(
    private readonly api: ApiClient,
    private readonly store: TokenStore,
  ) {}

  current(): AuthState {
    return this.state;
  }

  subscribe(listener: (state: AuthState) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private set(state: AuthState): void {
    this.state = state;
    for (const listener of this.listeners) {
      listener(state);
    }
  }

  /**
   * Restores a session on launch.
   *
   * A stored token is not proof of a valid session, so the profile is fetched.
   * If that fails the tokens are discarded: leaving unusable tokens on the
   * device would only produce failed requests on every screen afterwards.
   */
  async restore(): Promise<void> {
    const tokens = await this.store.read();
    if (!tokens) {
      this.set({ status: 'anonymous' });
      return;
    }

    try {
      const user = (await this.api.me()) as User;
      this.set(
        user.emailVerified
          ? { status: 'authenticated', user }
          : { status: 'unverified', user },
      );
    } catch (err) {
      if (err instanceof ApiError && err.requiresSignIn) {
        // The refresh already ran and failed, so the store is clear.
        this.set({ status: 'anonymous' });
        return;
      }
      // A network blip should not sign the user out; the session may still be
      // good once the connection returns.
      this.set({
        status: 'error',
        message: 'Could not reach TripVault. Check your connection and try again.',
      });
    }
  }

  async signIn(email: string, password: string, deviceId?: string): Promise<void> {
    const result = await this.api.login({
      email,
      password,
      ...(deviceId ? { deviceId } : {}),
    });
    await this.adopt(result);
  }

  async register(input: { email: string; password: string; name: string }): Promise<void> {
    // Registration deliberately does not sign the user in: the server requires
    // a verified email first, and pretending otherwise would show an app full of
    // screens that cannot load.
    await this.api.register(input);
  }

  async verifyEmail(token: string): Promise<void> {
    await this.api.verifyEmail(token);

    // Verification does not return a token pair, so sign in again to get one.
    this.set({ status: 'anonymous' });
  }

  /** Asks for another verification email. The answer is the same either way. */
  async resendVerification(email: string): Promise<void> {
    await this.api.resendVerification(email);
  }

  /**
   * Asks for a password reset link.
   *
   * Deliberately does not change the session state. The server answers the same
   * way whether or not the address has an account, so there is nothing here that
   * tells the caller anything, and treating a request as a sign-in or a sign-out
   * would leak more than the API does.
   */
  async requestPasswordReset(email: string): Promise<void> {
    await this.api.forgotPassword(email);
  }

  /**
   * Completes a password reset from an emailed token.
   *
   * The server revokes every session for the account, including this one, so the
   * local tokens are dropped here. Anything less would leave a device holding a
   * token the server has already invalidated.
   */
  async resetPassword(token: string, password: string): Promise<void> {
    await this.api.resetPassword(token, password);
    await this.store.clear();
    this.set({ status: 'anonymous' });
  }

  async signOut(): Promise<void> {
    await this.api.logout();
    this.set({ status: 'anonymous' });
  }

  private async adopt(result: AuthResponse): Promise<void> {
    if (!result.user) {
      throw new ApiError({
        code: ErrorCode.internalError,
        message: 'The server sent a response the app could not read.',
        status: 0,
      });
    }

    // Persist before announcing the new state. If the write fails the user stays
    // signed out, which is recoverable, rather than being shown a signed-in app
    // that forgets the session on the next launch.
    await this.store.write(result.tokens);
    this.set(
      result.user.emailVerified
        ? { status: 'authenticated', user: result.user }
        : { status: 'unverified', user: result.user },
    );
  }
}
