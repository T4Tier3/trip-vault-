import { parseDeepLink } from '../src/auth/deepLink';

describe('parseDeepLink', () => {
  const scheme = 'tripvault';

  it('reads a verification link from the API', () => {
    // This is the exact shape backend/internal/emailserver builds.
    expect(parseDeepLink('tripvault://verify-email?token=abc123', scheme)).toEqual({
      screen: 'verify-email',
      token: 'abc123',
    });
  });

  it('reads a password reset link', () => {
    expect(parseDeepLink('tripvault://reset-password?token=xyz', scheme)).toEqual({
      screen: 'reset-password',
      token: 'xyz',
    });
  });

  it('ignores extra parameters the server might add later', () => {
    expect(parseDeepLink('tripvault://verify-email?token=abc&utm_source=email', scheme)).toEqual({
      screen: 'verify-email',
      token: 'abc',
    });
  });

  it('rejects a link with no token instead of opening a broken screen', () => {
    expect(parseDeepLink('tripvault://verify-email', scheme)).toEqual({
      screen: 'unknown',
      token: null,
    });
    expect(parseDeepLink('tripvault://verify-email?token=', scheme)).toEqual({
      screen: 'unknown',
      token: null,
    });
  });

  it('rejects a link from a different scheme', () => {
    // A link for another app must never be treated as ours.
    expect(parseDeepLink('https://example.com/verify-email?token=abc', scheme)).toEqual({
      screen: 'unknown',
      token: null,
    });
    expect(parseDeepLink('otherapp://verify-email?token=abc', scheme)).toEqual({
      screen: 'unknown',
      token: null,
    });
  });

  it('rejects a screen it does not know', () => {
    expect(parseDeepLink('tripvault://somewhere-else?token=abc', scheme)).toEqual({
      screen: 'unknown',
      token: null,
    });
  });

  it('survives malformed input without throwing', () => {
    for (const input of ['', 'tripvault://', '::::', 'tripvault://verify-email?']) {
      expect(() => parseDeepLink(input, scheme)).not.toThrow();
    }
  });

  it('handles a token that needed escaping', () => {
    // The API percent-encodes the token; a token with an encoded ampersand must
    // not be split into a second parameter.
    expect(parseDeepLink('tripvault://reset-password?token=a%26b%3Dc', scheme)).toEqual({
      screen: 'reset-password',
      token: 'a&b=c',
    });
  });
});
