import type { TokenPair } from '../api/types';

/**
 * Where the client keeps its session.
 *
 * An interface rather than a concrete secure-storage call, so the API client can
 * be tested without a device and so the storage choice is one decision in one
 * place.
 */
export interface TokenStore {
  read(): Promise<TokenPair | null>;
  write(tokens: TokenPair): Promise<void>;
  clear(): Promise<void>;
}

/**
 * An in-memory store for tests and for the window before the real one is ready.
 *
 * Note what it cannot do: nothing survives a restart, so it must never be the
 * production implementation.
 */
export class MemoryTokenStore implements TokenStore {
  private tokens: TokenPair | null = null;

  constructor(initial: TokenPair | null = null) {
    this.tokens = initial;
  }

  async read(): Promise<TokenPair | null> {
    return this.tokens;
  }

  async write(tokens: TokenPair): Promise<void> {
    this.tokens = tokens;
  }

  async clear(): Promise<void> {
    this.tokens = null;
  }
}
