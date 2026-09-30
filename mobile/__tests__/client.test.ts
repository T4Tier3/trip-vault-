import { ApiClient, type FetchLike, type FetchLikeResponse } from '../src/api/client';
import { ApiError, ErrorCode, NetworkError, TimeoutError } from '../src/api/errors';
import { MemoryTokenStore } from '../src/auth/tokenStore';
import type { TokenPair } from '../src/api/types';

const BASE = 'https://api.tripvault.test';

function tokenPair(access: string, refresh: string, expiresIn = 900): TokenPair {
  return {
    accessToken: access,
    refreshToken: refresh,
    tokenType: 'Bearer',
    expiresAt: new Date(Date.now() + expiresIn * 1000).toISOString(),
    expiresIn,
  };
}

interface QueuedResponse {
  status: number;
  body?: unknown;
  headers?: Record<string, string>;
}

interface Call {
  path: string;
  method: string;
  body: unknown;
  authorization: string | undefined;
}

/**
 * A scriptable fetch that records what was sent and replies from a queue of
 * responses.
 */
class FakeFetch {
  readonly calls: Call[] = [];
  private queue: QueuedResponse[] = [];
  private fallback: QueuedResponse = { status: 200, body: { data: {} } };

  enqueue(status: number, body?: unknown, headers?: Record<string, string>): this {
    this.queue.push({ status, body, headers });
    return this;
  }

  setFallback(status: number, body?: unknown): this {
    this.fallback = { status, body };
    return this;
  }

  get fetch(): FetchLike {
    return async (input, init) => {
      this.calls.push({
        path: input,
        method: init?.method ?? 'GET',
        body: init?.body ? JSON.parse(init.body) : undefined,
        authorization: init?.headers?.Authorization,
      });

      const next = this.queue.shift() ?? this.fallback;
      const text = next.body === undefined ? '' : JSON.stringify(next.body);
      // HTTP header names are case-insensitive, so normalise on the way in.
      const headers = new Map<string, string>(
        Object.entries(next.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v]),
      );
      headers.set('content-type', 'application/json');

      const response: FetchLikeResponse = {
        ok: next.status >= 200 && next.status < 300,
        status: next.status,
        headers: { get: (name) => headers.get(name.toLowerCase()) ?? null },
        text: async () => text,
      };
      return response;
    };
  }

  countFor(pathFragment: string): number {
    return this.calls.filter((c) => c.path.includes(pathFragment)).length;
  }
}

/** The 401 body the server sends once an access token is no longer valid. */
const EXPIRED = { error: { code: ErrorCode.tokenExpired, message: 'Your session has expired.' } };

function client(fake: FakeFetch, store: MemoryTokenStore, onSignedOut?: () => void): ApiClient {
  return new ApiClient({ baseUrl: BASE, store, fetchImpl: fake.fetch, onSignedOut });
}

describe('ApiClient request handling', () => {
  it('sends the access token as a bearer credential', async () => {
    const fake = new FakeFetch().enqueue(200, { data: { id: 'u1' } });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));

    const result = await client(fake, store).request<{ id: string }>('/users/me');

    expect(result).toEqual({ id: 'u1' });
    expect(fake.calls[0]?.authorization).toBe('Bearer access-1');
  });

  it('refuses to make an authenticated call with no session', async () => {
    const fake = new FakeFetch();
    const store = new MemoryTokenStore(null);

    // No HTTP call should be attempted at all.
    await expect(client(fake, store).request('/users/me')).rejects.toMatchObject({
      code: ErrorCode.unauthorized,
    });
    expect(fake.calls).toHaveLength(0);
  });

  it('omits the token for anonymous endpoints', async () => {
    const fake = new FakeFetch().enqueue(200, { data: { message: 'ok' } });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));

    await client(fake, store).login({ email: 'a@b.test', password: 'secret' });

    expect(fake.calls[0]?.authorization).toBeUndefined();
  });

  it('unwraps the data envelope', async () => {
    const fake = new FakeFetch().enqueue(200, { data: { name: 'Saurabh' } });
    const store = new MemoryTokenStore(tokenPair('a', 'r'));

    await expect(client(fake, store).request<{ name: string }>('/users/me')).resolves.toEqual({
      name: 'Saurabh',
    });
  });

  it('reports a missing session as unauthorized', async () => {
    const fake = new FakeFetch();
    const store = new MemoryTokenStore(null);

    await expect(client(fake, store).request('/users/me')).rejects.toBeInstanceOf(ApiError);
  });
});

describe('ApiClient refresh', () => {
  it('refreshes once and replays the request after a 401', async () => {
    const fake = new FakeFetch()
      .enqueue(401, EXPIRED)
      .enqueue(200, { data: { tokens: tokenPair('access-2', 'refresh-2'), user: { id: 'u1' } } })
      .enqueue(200, { data: { id: 'u1' } });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));

    const result = await client(fake, store).request<{ id: string }>('/users/me');

    expect(result).toEqual({ id: 'u1' });
    // The replay must use the new token, not the one that just failed.
    expect(fake.calls[2]?.authorization).toBe('Bearer access-2');
    expect(await store.read()).toMatchObject({ accessToken: 'access-2', refreshToken: 'refresh-2' });
  });

  it('collapses a burst of 401s into a single refresh call', async () => {
    // Every request 401s first, and there is exactly one refresh response
    // available. If the client issued more than one refresh, the extra ones
    // would fall through to the fallback and the test would fail.
    const fake = new FakeFetch().setFallback(200, { data: { ok: true } });
    for (let i = 0; i < 5; i += 1) {
      fake.enqueue(401, EXPIRED);
    }
    fake.enqueue(200, { data: { tokens: tokenPair('access-2', 'refresh-2'), user: {} } });
    for (let i = 0; i < 5; i += 1) {
      fake.enqueue(200, { data: { ok: true } });
    }

    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));
    const api = client(fake, store);

    const results = await Promise.all([
      api.request('/users/me'),
      api.request('/users/me'),
      api.request('/users/me'),
      api.request('/users/me'),
      api.request('/users/me'),
    ]);

    expect(results).toHaveLength(5);
    // This is the assertion that matters: more than one refresh here would mean
    // the server sees a reused refresh token and revokes the whole family.
    expect(fake.countFor('/auth/refresh')).toBe(1);
  });

  it('does not retry a request the server rejected for a non-auth reason', async () => {
    const fake = new FakeFetch().enqueue(422, {
      error: { code: ErrorCode.validationFailed, message: 'Check the form.', fields: { name: 'too long' } },
    });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));

    await expect(client(fake, store).request('/users/me')).rejects.toMatchObject({
      code: ErrorCode.validationFailed,
      fields: { name: 'too long' },
    });
    // Retrying would double any side effect and could not succeed anyway.
    expect(fake.calls).toHaveLength(1);
  });

  it('clears the session and notifies when the refresh token is rejected', async () => {
    const fake = new FakeFetch()
      .enqueue(401, EXPIRED)
      .enqueue(401, { error: { code: ErrorCode.tokenInvalid, message: 'Session not found.' } });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));
    const onSignedOut = jest.fn();

    await expect(client(fake, store, onSignedOut).request('/users/me')).rejects.toMatchObject({
      code: ErrorCode.tokenInvalid,
    });

    expect(await store.read()).toBeNull();
    expect(onSignedOut).toHaveBeenCalledTimes(1);
  });

  it('recovers on the next call after a failed refresh rather than wedging', async () => {
    const fake = new FakeFetch()
      .enqueue(401, EXPIRED)
      .enqueue(401, { error: { code: ErrorCode.tokenInvalid, message: 'nope' } })
      .enqueue(401, EXPIRED)
      .enqueue(200, { data: { tokens: tokenPair('access-3', 'refresh-3'), user: {} } })
      .enqueue(200, { data: { ok: true } });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));
    const api = client(fake, store);

    await expect(api.request('/users/me')).rejects.toBeInstanceOf(ApiError);

    // A stale in-flight promise here would make the second call fail too.
    store.write(tokenPair('access-3', 'refresh-3'));
    await expect(api.request('/users/me')).resolves.toEqual({ ok: true });
  });

  it('surfaces the rate limit and its Retry-After', async () => {
    const fake = new FakeFetch().enqueue(429, {
      error: { code: ErrorCode.rateLimited, message: 'Too many attempts.' },
    }, { 'Retry-After': '60' });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));

    const err = await client(fake, store)
      .request('/users/me')
      .catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).retryAfterSeconds).toBe(60);
    expect((err as ApiError).isRetryable).toBe(true);
  });
});

describe('ApiClient transport failures', () => {
  it('reports an unreachable server as a network error', async () => {
    const store = new MemoryTokenStore(tokenPair('a', 'r'));
    const api = new ApiClient({
      baseUrl: BASE,
      store,
      fetchImpl: async () => {
        throw new TypeError('Network request failed');
      },
    });

    await expect(api.request('/users/me')).rejects.toBeInstanceOf(NetworkError);
  });

  it('times out a request that never answers', async () => {
    const store = new MemoryTokenStore(tokenPair('a', 'r'));
    const api = new ApiClient({
      baseUrl: BASE,
      store,
      timeoutMs: 40,
      fetchImpl: (_input, init) =>
        // A server that accepts the connection and then says nothing, which is
        // what a wedged backend looks like from the device.
        new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new Error('aborted')));
        }),
    });

    await expect(api.request('/users/me')).rejects.toBeInstanceOf(TimeoutError);
  });

  it('aborts when the caller cancels, without reporting a network fault', async () => {
    const store = new MemoryTokenStore(tokenPair('a', 'r'));
    const controller = new AbortController();
    const api = new ApiClient({
      baseUrl: BASE,
      store,
      timeoutMs: 10_000,
      fetchImpl: (_input, init) =>
        new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new Error('aborted')));
        }),
    });

    const pending = api.request('/users/me', { signal: controller.signal });
    controller.abort();

    // A screen the user swiped away from must not raise an error toast.
    await expect(pending).rejects.toMatchObject({ code: 'aborted' });
  });

  it('turns a non-JSON error from a proxy into a readable message', async () => {
    const fake = new FakeFetch().enqueue(502, undefined);
    const store = new MemoryTokenStore(tokenPair('a', 'r'));

    const err = await client(fake, store)
      .request('/users/me')
      .catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe(ErrorCode.internalError);
    expect((err as ApiError).message).not.toContain('502');
  });
});

describe('ApiClient logout', () => {
  it('clears the local session and revokes it server side', async () => {
    const fake = new FakeFetch().enqueue(200, { data: { message: 'ok' } });
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));

    await client(fake, store).logout();

    expect(await store.read()).toBeNull();
    expect(fake.calls[0]?.path).toContain('/auth/logout');
    expect(fake.calls[0]?.body).toEqual({ refreshToken: 'refresh-1' });
  });

  it('still signs the user out locally when the network is down', async () => {
    const store = new MemoryTokenStore(tokenPair('access-1', 'refresh-1'));
    const api = new ApiClient({
      baseUrl: BASE,
      store,
      fetchImpl: async () => {
        throw new TypeError('Network request failed');
      },
    });

    await api.logout();

    // The device must not keep a token because the server was unreachable.
    expect(await store.read()).toBeNull();
  });
});
