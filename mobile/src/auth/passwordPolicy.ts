/**
 * The password rules, mirroring the server's policy.
 *
 * This exists so the form can say what is wrong before a round trip, and so
 * registration and password reset cannot drift apart from each other. It is a
 * convenience, never the authority: the server re-checks everything, so
 * loosening a rule here can only delay the complaint, never weaken a password.
 *
 * Two deliberate omissions from the server's policy:
 *
 *  - The breach denylist is not copied. It is a judgement call the server makes
 *    on every attempt, and shipping a partial copy would only make the app
 *    disagree with the server while giving a false sense of having checked.
 *  - The variety threshold is two character classes, not three, and that is
 *    intentional on both sides. Requiring three rejects a long passphrase built
 *    entirely from lower case, which is a perfectly good password, and pushes
 *    people toward predictable substitutions like "Passw0rd!".
 */

/** Matches auth.MinPasswordLength on the server. */
export const MIN_LENGTH = 10;

/**
 * Matches auth.MaxPasswordLength, which bounds the hashing cost.
 *
 * The server applies that limit in bytes, while this counts characters. A form
 * that tried to reproduce the byte arithmetic would only ever be right by
 * accident and would be useless to the person typing, so the two are close
 * enough that the difference is reported by the server if it ever matters.
 */
export const MAX_LENGTH = 256;

/** The fewest character classes a password may touch. Matches auth.variety. */
export const MIN_CLASSES = 2;

/**
 * Counts the character classes a password touches: lower case, upper case,
 * digits, and everything else as symbols.
 *
 * The unicode-aware property escapes are what keep a non-ASCII letter from being
 * miscounted as a symbol, which would let a password of accented characters pass
 * the variety check for the wrong reason.
 */
export function characterClasses(password: string): number {
  return [
    /\p{Ll}/u,
    /\p{Lu}/u,
    /\p{Nd}/u,
    /[^\p{L}\p{N}]/u,
  ].filter((pattern) => pattern.test(password)).length;
}

/**
 * Checks a password, returning the message to show or undefined when it passes.
 *
 * Messages match the server's `PasswordPolicyError` wording so that a client
 * that validates locally and a server that rejects anyway read identically.
 */
export function checkPassword(password: string): string | undefined {
  if (password.length === 0) {
    return 'Choose a password.';
  }
  if ([...password].length < MIN_LENGTH) {
    return `Must be at least ${MIN_LENGTH} characters.`;
  }
  if ([...password].length > MAX_LENGTH) {
    return `Must be at most ${MAX_LENGTH} characters.`;
  }
  if (characterClasses(password) < MIN_CLASSES) {
    return 'Must mix letters, digits or symbols.';
  }
  return undefined;
}
