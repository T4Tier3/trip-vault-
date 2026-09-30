import * as Keychain from 'react-native-keychain';

import type { TokenStore } from './tokenStore';
import type { TokenPair } from '../api/types';

const SERVICE = 'com.tripvault.session';

/**
 * Stores the session in the device keychain or keystore.
 *
 * This matters more than it looks. The refresh token is a long-lived bearer
 * credential: anything that can read it can act as the user until it expires or
 * is revoked. AsyncStorage, which is ordinary unencrypted files, would put that
 * credential in a backup or on a jailbroken device. Keychain/Keystore keeps it
 * in the platform's protected storage and, on iOS, ties it to the app's
 * accessibility class so it is not readable while the device is locked.
 */
export class SecureTokenStore implements TokenStore {
  async read(): Promise<TokenPair | null> {
    const entry = await Keychain.getGenericPassword({ service: SERVICE });

    if (!entry) {
      return null;
    }

    try {
      // A stored value we cannot parse is treated as no session at all. Leaving
      // it in place would fail every subsequent request in a confusing way.
      return JSON.parse(entry.password) as TokenPair;
    } catch {
      await this.clear();
      return null;
    }
  }

  async write(tokens: TokenPair): Promise<void> {
    await Keychain.setGenericPassword('session', JSON.stringify(tokens), {
      service: SERVICE,
      // Available only after the user has unlocked the device once since boot.
      accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
    });
  }

  async clear(): Promise<void> {
    await Keychain.resetGenericPassword({ service: SERVICE });
  }
}
