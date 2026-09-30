import {
  MAX_LENGTH,
  MIN_CLASSES,
  MIN_LENGTH,
  characterClasses,
  checkPassword,
} from '../src/auth/passwordPolicy';

/**
 * These tests exist to keep the client in step with the Go policy in
 * backend/internal/auth/password_policy.go.
 *
 * The pair that matters most is the variety threshold. The client once required
 * three character classes while the server required two, so a long all-lower-case
 * passphrase was accepted by the API and refused by the form in front of it.
 * A stricter client than the server is invisible until someone tries a
 * legitimate password, so the threshold is pinned here on both sides.
 */
describe('password policy', () => {
  it('uses the same bounds as the server', () => {
    // auth.MinPasswordLength and auth.MaxPasswordLength.
    expect(MIN_LENGTH).toBe(10);
    expect(MAX_LENGTH).toBe(256);
    expect(MIN_CLASSES).toBe(2);
  });

  describe('length', () => {
    it('asks for a password when there is none', () => {
      expect(checkPassword('')).toBe('Choose a password.');
    });

    it('refuses fewer than the minimum', () => {
      expect(checkPassword('aB3defgh')).toBe('Must be at least 10 characters.');
    });

    it('accepts exactly the minimum', () => {
      expect(checkPassword('abc123abc1')).toBeUndefined();
    });

    it('refuses more than the maximum', () => {
      expect(checkPassword('abc123abc1'.repeat(30))).toBe(`Must be at most ${MAX_LENGTH} characters.`);
    });

    it('counts characters rather than UTF-16 units', () => {
      // Five emoji are five characters, but ten UTF-16 units. Counting units
      // would call this long enough while the server, which counts runes,
      // would call it too short and reject it.
      const fiveEmoji = '\u{1F3F5}\u{1F3F5}\u{1F3F5}\u{1F3F5}\u{1F3F5}';
      expect(fiveEmoji).toHaveLength(10);
      expect([...fiveEmoji]).toHaveLength(5);
      expect(checkPassword(fiveEmoji)).toBe('Must be at least 10 characters.');
    });

    it('does not fault the length of a multi-byte password', () => {
      // Ten accented characters, which is 20 bytes in UTF-8. The complaint here
      // must be about variety, not length: if the length rule were counting
      // bytes, this would report the wrong problem to the user.
      const tenAccents = 'é'.repeat(MIN_LENGTH);
      expect(checkPassword(tenAccents)).toBe('Must mix letters, digits or symbols.');

      // And the same with digits added, which clears the variety rule and so
      // passes outright.
      expect(checkPassword(`${tenAccents}9`)).toBeUndefined();
    });
  });

  describe('variety', () => {
    it('refuses a password in a single character class', () => {
      expect(checkPassword('aaaaaaaaaaaaaaaaaaaa')).toBe('Must mix letters, digits or symbols.');
    });

    it('accepts a passphrase in two classes, which is the point of the rule', () => {
      // All lower case plus digits. The server accepts this, so the form must.
      expect(checkPassword('correct horse battery staple 9')).toBeUndefined();
    });

    it('accepts letters mixed with symbols', () => {
      expect(checkPassword('monkey!monkey!monkey!')).toBeUndefined();
    });
  });

  describe('characterClasses', () => {
    it('counts each class once', () => {
      expect(characterClasses('aA1!')).toBe(4);
      expect(characterClasses('aa')).toBe(1);
      expect(characterClasses('123')).toBe(1);
      expect(characterClasses('!!!')).toBe(1);
      expect(characterClasses('')).toBe(0);
    });

    it('does not mistake an accented letter for a symbol', () => {
      // A regex without the unicode flag would classify these as symbols, which
      // would let a single-class password pass the variety check.
      expect(characterClasses('éàüñçÅØÆßı')).toBe(2);
    });
  });
});
