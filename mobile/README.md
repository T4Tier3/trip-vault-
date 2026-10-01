# TripVault mobile

The Expo client. This slice covers the authentication journey only: sign in,
register, confirm the email address, reset a forgotten password, and the session
that carries the tokens between screens.

Trips, albums, uploads and offline support are later phases. There is no trip
screen yet; a successful sign-in currently shows the signed-in user, which is
enough to prove the session round trip works end to end.

## Stack

Expo SDK 57 (React Native 0.86, React 19.2), TypeScript, `expo-router` is **not**
used: the auth journey is a handful of explicit states, so `src/screens/AuthFlow.tsx`
models them directly. A real navigator arrives with trips and albums.

The native `ios/` and `android/` directories are **not** committed. Expo generates
them from `app.json` via Continuous Native Generation, so `npx expo prebuild`
recreates them and there is nothing to merge when the SDK moves.

## Running it on a phone

The fastest path is Expo Go, and it needs no Android SDK or Xcode:

```bash
npm install
npm start
```

That prints a QR code. Install **Expo Go** from the Play Store or App Store, make
sure the phone is on the same network as this Mac, and scan it.

Two caveats, both worth knowing before you go looking for the bug:

- **Expo Go cannot receive `tripvault://` links.** It opens `exp://` links only.
  A custom scheme needs a development build, so cold-starting a verification or
  reset link has to be tested with `npx expo run:android` or an EAS build. The
  deep-link parsing itself is covered by tests, so the logic is not unverified
  just because Expo Go cannot exercise it.
- **Expo Go supports one SDK version at a time.** The Play Store build may still
  be on an older SDK than the project, in which case it refuses to open the
  bundle. `npx expo start --android` or a development build is the fallback.

To build a real APK on a connected phone, with USB debugging on:

```bash
npx expo run:android
```

The API must be reachable. `localhost` works on the iOS simulator and the Android
emulator maps it to the host, but on a **physical** phone it points at the phone
itself, so set the base URL in `src/config.ts` to this Mac's LAN address.

```bash
# from the repository root
docker compose up -d
cd backend && go run ./cmd/server
```

## Checks

```bash
npm test           # 55 tests
npm run typecheck  # tsc --noEmit
npm run lint       # eslint . (flat config, eslint-config-expo/flat)
npm run doctor     # expo doctor, 21 checks
```

The tests cover the parts where a mistake is silent: token refresh, error
mapping, the session state machine, deep link parsing, and the password rules.
`src/api/client.ts`, `src/auth/session.ts`, `src/auth/deepLink.ts` and
`src/auth/passwordPolicy.ts` import nothing from React Native or Expo, which is
what lets them run under Node with no simulator.

`src/auth/passwordPolicy.ts` mirrors the Go policy in
`backend/internal/auth/password_policy.go`. The client is the one place the two
can drift, and it did: the form once demanded three character classes while the
server demanded two, so a long all-lower-case passphrase was accepted by the API
and refused by the form in front of it. `__tests__/passwordPolicy.test.ts` pins
the numbers on both sides. The breach denylist is deliberately not copied to the
client; that check stays on the server.

## How the session works

Access tokens are short lived, so requests will start failing with 401 simply
because time passed. `ApiClient` handles that: on a 401 it refreshes once and
replays the request.

The part worth being careful about is concurrency. If several screens load at
once and the token has just expired, they all get a 401 together. Each one
calling `/auth/refresh` with the same refresh token would be a problem, because
the server rotates refresh tokens and treats reuse as theft: the first would
succeed and the rest would be rejected as a stolen token, signing the user out.
So refresh is single-flight, in `ApiClient.refresh`, and
`__tests__/client.test.ts` asserts that a burst of five 401s produces exactly
one refresh call.

Tokens live in the keychain or keystore through `expo-secure-store`, not in
AsyncStorage. The refresh token is a long-lived bearer credential, so it belongs
in protected storage.

## Deep links

The API emails links like `tripvault://verify-email?token=...`. The scheme is
declared as `expo.scheme` in `app.json`, and `expo prebuild` writes the native
registrations from it:

- **iOS** — `Info.plist` → `CFBundleURLTypes` → `CFBundleURLSchemes`, with
  `tripvault`.
- **Android** — an `intent-filter` in `AndroidManifest.xml` carrying the scheme
  in `android:scheme`, plus `android.intent.category.BROWSABLE` and
  `android.intent.category.DEFAULT`. The `DEFAULT` category is what allows an
  app that has not been installed to still be chosen as the handler.

That is the reason to change it in `app.json` and prebuild rather than editing
the native projects by hand: an edit is lost the next time anyone runs prebuild.

The scheme has to match `APP_LINK_SCHEME` on the API and `config.linkScheme` in
`src/config.ts`. If those three disagree, email links open a browser and show
JSON, and the only symptom is a support ticket saying "the link does not work".

`src/auth/deepLink.ts` parses the URLs and is covered by tests, including the
cases that must not be treated as valid: a different scheme, an unknown screen,
and a link with no token.

A link is treated as navigation, not just data: opening one switches the screen
in `src/screens/AuthFlow.tsx`, which is what makes a cold start work, since the
app can be launched by the link with no prior state. The link is cleared when
the screen is left, so finishing a reset does not re-open the reset screen from
a token still sitting in state.

A reset link typed on another device is the one case that cannot work: the token
lives in the URL, and the app has no way to read a link that went to a different
device. Ask for a new one from the sign-in screen.
