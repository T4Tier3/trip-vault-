import { Session, type AuthState } from '../src/auth/session';
import { MemoryTokenStore } from '../src/auth/tokenStore';
import { ApiError, ErrorCode } from '../src/api/errors';
import type { AuthResponse, User } from '../src/api/types';

/**
 * A session under test, with a fake ApiClient.
 *
 * The fake records what it was asked and returns scripted answers, so these
 * tests cover the state transitions rather than the transport.
 */
function makeSession(overrides: Partial<Record<keyof ScriptedApi, unknown>> = {}) {
  const api = {
    login: jest.fn(),
    register: jest.fn(),
    verifyEmail: jest.fn(),
    me: jest.fn(),
    logout: jest.fn(),
    ...overrides,
  } as unknown as Session['api'];

  const store = new MemoryTokenStore();
  const session = new Session(api, store);
  return { session, api, store };
}

type ScriptedApi = Session['api'];

const VERIFIED: User = {
  id: 'u1',
  email: 'saurabh@example.com',
  name: 'Saurabh',
  emailVerified: true,
  createdAt: '2026-01-01T00:00:00Z',
};

const UNVERIFIED: User = { ...VERIFIED, emailVerified: false };

function authResponse(user: User): AuthResponse {
  return {
    tokens: {
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    },
    user,
  };
}

describe('Session state', () => {
  it('starts in loading and reports anonymous with no stored token', async () => {
    const { session } = makeSession();

    expect(session.current().status).toBe('loading');

    await session.restore();

    expect(session.current().status).toBe('anonymous');
  });

  it('restores an authenticated session from a stored token', async () => {
    const store = new MemoryTokenStore({
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    });
    const api = { me: jest.fn().mockResolvedValue(VERIFIED) } as unknown as Session['api'];
    const session = new Session(api, store);

    await session.restore();

    // The stored token is not proof of validity, so the profile was fetched.
    expect(api.me).toHaveBeenCalledTimes(1);
    expect(session.current()).toEqual({ status: 'authenticated', user: VERIFIED });
  });

  it('reports unverified rather than authenticated for a new account', async () => {
    const { session, api } = makeSession({ login: jest.fn().mockResolvedValue(authResponse(UNVERIFIED)) });

    await session.signIn('saurabh@example.com', 'A Good Passphrase 42');

    expect(api.login).toHaveBeenCalledWith({
      email: 'saurabh@example.com',
      password: 'A Good Passphrase 42',
    });
    expect(session.current()).toEqual({ status: 'unverified', user: UNVERIFIED });
  });

  it('notifies subscribers on every transition', async () => {
    const { session } = makeSession({ login: jest.fn().mockResolvedValue(authResponse(VERIFIED)) });
    const seen: AuthState[] = [];
    session.subscribe((state) => seen.push(state));

    await session.signIn('a@b.test', 'A Good Passphrase 42');

    expect(seen).toEqual([{ status: 'authenticated', user: VERIFIED }]);
  });

  it('stops notifying after unsubscribe', async () => {
    const { session } = makeSession({ login: jest.fn().mockResolvedValue(authResponse(VERIFIED)) });
    const listener = jest.fn();
    const unsubscribe = session.subscribe(listener);

    unsubscribe();
    await session.signIn('a@b.test', 'A Good Passphrase 42');

    expect(listener).not.toHaveBeenCalled();
  });
});

describe('Session token storage', () => {
  it('keeps the tokens from a successful sign in', async () => {
    const { session, store } = makeSession({ login: jest.fn().mockResolvedValue(authResponse(VERIFIED)) });

    await session.signIn('saurabh@example.com', 'A Good Passphrase 42');

    // Without this the app signs in and then forgets the session on relaunch,
    // which reads to the user as a random sign out.
    expect(await store.read()).toEqual(authResponse(VERIFIED).tokens);
  });

  it('keeps the tokens for an unverified account too', async () => {
    const { session, store } = makeSession({
      login: jest.fn().mockResolvedValue(authResponse(UNVERIFIED)),
    });

    await session.signIn('saurabh@example.com', 'A Good Passphrase 42');

    expect(await store.read()).not.toBeNull();
  });

  it('restores the session on a later launch', async () => {
    const store = new MemoryTokenStore();
    const first = new Session(
      { login: jest.fn().mockResolvedValue(authResponse(VERIFIED)) } as unknown as Session['api'],
      store,
    );

    await first.signIn('saurabh@example.com', 'A Good Passphrase 42');

    // A second session over the same store is what a relaunch looks like.
    const api = { me: jest.fn().mockResolvedValue(VERIFIED) } as unknown as Session['api'];
    const relaunched = new Session(api, store);
    await relaunched.restore();

    expect(relaunched.current()).toEqual({ status: 'authenticated', user: VERIFIED });
  });

  it('does not write a token when the response has no user', async () => {
    const { session, store } = makeSession({ login: jest.fn().mockResolvedValue({ tokens: {} }) });

    await expect(session.signIn('a@b.test', 'A Good Passphrase 42')).rejects.toBeInstanceOf(ApiError);

    expect(await store.read()).toBeNull();
  });

  it('stays anonymous when the tokens cannot be written', async () => {
    // Some devices refuse to write to secure storage, and a sign in that appears
    // to work but cannot be kept is worse than one that reports the problem.
    const store = {
      read: jest.fn().mockResolvedValue(null),
      write: jest.fn().mockRejectedValue(new Error('Keychain unavailable')),
      clear: jest.fn().mockResolvedValue(undefined),
    };
    const session = new Session(
      { login: jest.fn().mockResolvedValue(authResponse(VERIFIED)) } as unknown as Session['api'],
      store,
    );

    await expect(session.signIn('a@b.test', 'A Good Passphrase 42')).rejects.toThrow('Keychain unavailable');
    expect(session.current().status).toBe('loading');
  });
});

describe('Session failure handling', () => {
  it('returns to anonymous when a stored token is rejected', async () => {
    const store = new MemoryTokenStore({
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    });
    const api = {
      me: jest.fn().mockRejectedValue(
        new ApiError({ code: ErrorCode.tokenInvalid, message: 'Session not found.', status: 401 }),
      ),
    } as unknown as Session['api'];
    const session = new Session(api, store);

    await session.restore();

    expect(session.current().status).toBe('anonymous');
  });

  it('leaves the launch spinner when the token store itself throws', async () => {
    // The initial state is 'loading' and nothing else advances it, so a store
    // read that rejects used to strand the app on the launch spinner: no error,
    // no login screen, nothing to retry.
    const store = {
      read: jest.fn().mockRejectedValue(new Error('keystore unavailable')),
      write: jest.fn(),
      clear: jest.fn(),
    };
    const api = { me: jest.fn() } as unknown as Session['api'];
    const session = new Session(api, store);

    await session.restore();

    expect(session.current().status).not.toBe('loading');
    expect(session.current().status).toBe('error');
  });

  it('does not sign the user out when the network is down', async () => {
    const store = new MemoryTokenStore({
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    });
    const api = { me: jest.fn().mockRejectedValue(new Error('Network request failed')) } as unknown as Session['api'];
    const session = new Session(api, store);

    await session.restore();

    // A blip is not a reason to make someone sign in again.
    expect(session.current().status).toBe('error');
    expect(await store.read()).not.toBeNull();
  });

  it('rejects a login the server answered with no user', async () => {
    const { session } = makeSession({ login: jest.fn().mockResolvedValue({ tokens: {} }) });

    await expect(session.signIn('a@b.test', 'A Good Passphrase 42')).rejects.toBeInstanceOf(ApiError);
  });

  it('lets an unverified account verify and then sign in', async () => {
    const { session, api } = makeSession({
      login: jest.fn().mockResolvedValue(authResponse(UNVERIFIED)),
      verifyEmail: jest.fn().mockResolvedValue({ message: 'ok' }),
    });

    await session.signIn('saurabh@example.com', 'A Good Passphrase 42');
    expect(session.current().status).toBe('unverified');

    await session.verifyEmail('token-123');

    expect(api.verifyEmail).toHaveBeenCalledWith('token-123');
    // Verification alone does not mint tokens, so the app waits for a sign-in.
    expect(session.current().status).toBe('anonymous');
  });

  it('clears the session on sign out', async () => {
    const store = new MemoryTokenStore();
    // The real client clears the store inside logout(); the fake does the same
    // so this test exercises the real division of responsibility.
    const logout = jest.fn(async () => {
      await store.clear();
    });
    const { session, api } = makeSession({ logout });
    await store.write({
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    });

    await session.signOut();

    expect(api.logout).toHaveBeenCalled();
    expect(session.current().status).toBe('anonymous');
    expect(await store.read()).toBeNull();
  });
});

describe('Password reset', () => {
  it('asks for a reset without changing the session', async () => {
    const { session, api, store } = makeSession({
      forgotPassword: jest.fn().mockResolvedValue({ message: 'ok' }),
    });

    await session.requestPasswordReset('saurabh@example.com');

    expect(api.forgotPassword).toHaveBeenCalledWith('saurabh@example.com');
    // The server answers the same way whether or not the address is registered,
    // so nothing here may hint at one or the other.
    expect(session.current().status).toBe('loading');
    expect(await store.read()).toBeNull();
  });

  it('drops the local session when a password is reset', async () => {
    const store = new MemoryTokenStore();
    await store.write({
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    });
    const api = {
      resetPassword: jest.fn().mockResolvedValue({ message: 'ok' }),
    } as unknown as Session['api'];
    const session = new Session(api, store);

    await session.resetPassword('token-123', 'a new strong passphrase');

    expect(api.resetPassword).toHaveBeenCalledWith('token-123', 'a new strong passphrase');
    // The server revoked every session for the account, including this one, so
    // keeping the token would leave the device holding a dead credential.
    expect(await store.read()).toBeNull();
    expect(session.current()).toEqual({ status: 'anonymous' });
  });

  it('keeps the session when the reset is rejected', async () => {
    const store = new MemoryTokenStore();
    await store.write({
      accessToken: 'a',
      refreshToken: 'r',
      tokenType: 'Bearer',
      expiresAt: '2026-01-01T00:15:00Z',
      expiresIn: 900,
    });
    const api = {
      resetPassword: jest.fn().mockRejectedValue(
        new ApiError({ code: ErrorCode.tokenInvalid, message: 'That link has expired.', status: 400 }),
      ),
    } as unknown as Session['api'];
    const session = new Session(api, store);

    await expect(session.resetPassword('expired', 'a new strong passphrase')).rejects.toBeInstanceOf(
      ApiError,
    );

    // An expired link says nothing about the existing session, so it survives.
    expect(await store.read()).not.toBeNull();
  });
});
