import * as SecureStore from 'expo-secure-store';

import type { TokenStore } from './tokenStore';
import type { TokenPair } from '../api/types';

const SESSION_KEY = 'com.tripvault.session';

/**
 * Stores the session in the device keychain or keystore.
 *
 * This matters more than it looks. The refresh token is a long-lived bearer
 * credential: anything that can read it can act as the user until it expires or
 * is revoked. AsyncStorage, which is ordinary unencrypted files, would put that
 * credential in a backup or on a jailbroken device. SecureStore keeps it in the
 * platform's protected storage and, on iOS, ties it to an accessibility class
 * so it is not readable while the device is locked.
 */
export class SecureTokenStore implements TokenStore {
  async read(): Promise<TokenPair | null> {
    const raw = await SecureStore.getItemAsync(SESSION_KEY);

    if (raw === null) {
      return null;
    }

    try {
      // A stored value we cannot parse is treated as no session at all. Leaving
      // it in place would fail every subsequent request in a confusing way.
      return JSON.parse(raw) as TokenPair;
    } catch {
      await this.clear();
      return null;
    }
  }

  async write(tokens: TokenPair): Promise<void> {
    await SecureStore.setItemAsync(SESSION_KEY, JSON.stringify(tokens), {
      // Available only after the user has unlocked the device once since boot,
      // and not migrated to a new device by a backup.
      keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
    });
  }

  async clear(): Promise<void> {
    await SecureStore.deleteItemAsync(SESSION_KEY);
  }
}
