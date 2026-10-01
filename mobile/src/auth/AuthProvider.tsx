import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';

import { ApiClient } from '../api/client';
import type { User } from '../api/types';
import { config } from '../config';
import { SecureTokenStore } from './secureTokenStore';
import { Session, type AuthState } from './session';
import type { TokenStore } from './tokenStore';

interface AuthContextValue {
  state: AuthState;
  signIn(email: string, password: string): Promise<void>;
  register(input: { email: string; password: string; name: string }): Promise<void>;
  verifyEmail(token: string): Promise<void>;
  resendVerification(email: string): Promise<void>;
  requestPasswordReset(email: string): Promise<void>;
  resetPassword(token: string, password: string): Promise<void>;
  signOut(): Promise<void>;
  retry(): Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

/**
 * Wires the session to React and exposes it to the screens.
 *
 * All the interesting logic lives in Session, which has no React in it and is
 * tested directly. This file only deals with subscription and lifetime.
 */
export function AuthProvider({
  children,
  // Injectable so an app-level test can supply an in-memory store.
  store,
}: {
  children: React.ReactNode;
  store?: TokenStore;
}) {
  const [state, setState] = useState<AuthState>({ status: 'loading' });

  const session = useMemo(() => {
    const tokenStore = store ?? new SecureTokenStore();
    const api = new ApiClient({
      baseUrl: config.apiBaseUrl,
      store: tokenStore,
      timeoutMs: config.requestTimeoutMs,
      // A refresh that fails is a dead session, so drop straight to the login
      // screen rather than leaving the user on a screen that cannot load.
      onSignedOut: () => setState({ status: 'anonymous' }),
    });
    return new Session(api, tokenStore);
  }, [store]);

  useEffect(() => session.subscribe(setState), [session]);

  useEffect(() => {
    void session.restore();
  }, [session]);

  // Note the absence of `setState({ status: 'loading' })` here and below.
  // 'loading' means "restoring a session at launch" and AuthFlow replaces the
  // whole screen with a spinner for it. Using it for a form submission therefore
  // unmounts the very screen that owns the error message, so a failed sign-in
  // set 'loading' and never unset it: the user was left staring at a spinner
  // instead of "wrong password". Each screen already has its own in-flight state.
  const signIn = useCallback(
    (email: string, password: string) => session.signIn(email, password),
    [session],
  );

  const register = useCallback(
    (input: { email: string; password: string; name: string }) => session.register(input),
    [session],
  );

  const verifyEmail = useCallback(
    async (token: string) => {
      await session.verifyEmail(token);
    },
    [session],
  );

  const resendVerification = useCallback(
    async (email: string) => {
      await session.resendVerification(email);
    },
    [session],
  );

  const requestPasswordReset = useCallback(
    async (email: string) => {
      await session.requestPasswordReset(email);
    },
    [session],
  );

  const resetPassword = useCallback(
    async (token: string, password: string) => {
      await session.resetPassword(token, password);
    },
    [session],
  );

  const signOut = useCallback(async () => {
    await session.signOut();
  }, [session]);

  const retry = useCallback(async () => {
    setState({ status: 'loading' });
    await session.restore();
  }, [session]);

  const value = useMemo<AuthContextValue>(
    () => ({
      state,
      signIn,
      register,
      verifyEmail,
      resendVerification,
      requestPasswordReset,
      resetPassword,
      signOut,
      retry,
    }),
    [
      state,
      signIn,
      register,
      verifyEmail,
      resendVerification,
      requestPasswordReset,
      resetPassword,
      signOut,
      retry,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/**
 * Reads the session.
 *
 * Throws rather than returning null when used outside the provider, so a
 * misplaced call fails at the first render instead of silently rendering a
 * broken screen.
 */
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used inside an AuthProvider');
  }
  return ctx;
}

/** The signed-in user, or null when there is no session. */
export function useCurrentUser(): User | null {
  const { state } = useAuth();
  return state.status === 'authenticated' || state.status === 'unverified' ? state.user : null;
}
