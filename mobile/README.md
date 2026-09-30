# TripVault mobile

The React Native client. This slice covers the authentication journey only:
sign in, register, confirm the email address, reset a forgotten password, and
the session that carries the tokens between screens.

Trips, albums, uploads and offline support are later phases. There is no trip
screen yet; a successful sign-in currently shows the signed-in user, which is
enough to prove the session round trip works end to end.

## What is here, and what is not

The TypeScript under `src/` is the deliverable. The native `ios/` and `android/`
projects are **not** committed, because they are generated per-machine and are
large. Generate them once with the command below, then commit the two scheme
registrations described in *Deep links*.

```bash
cd mobile
npx @react-native-community/cli@latest init TripVault --directory . --skip-install
```

That will overwrite `package.json` and add native folders; keep the contents of
`src/` and `__tests__/`, and re-apply the two changes noted below.

## Running it

```bash
npm install
npm start            # Metro
npm run ios          # or: npm run android
```

The API must be reachable. On the iOS simulator `localhost` works; on an Android
emulator the host is `10.0.2.2`, so set the base URL in `src/config.ts`
accordingly.

```bash
# from the repository root
docker compose up -d
cd backend && go run ./cmd/server
```

## Checks

```bash
npm test           # 55 tests
npm run typecheck  # tsc --noEmit
```

The tests cover the parts where a mistake is silent: token refresh, error
mapping, the session state machine, deep link parsing, and the password rules.
`src/api/client.ts`, `src/auth/session.ts`, `src/auth/deepLink.ts` and
`src/auth/passwordPolicy.ts` import nothing from React Native, which is what
lets them run under plain Node with no simulator.

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

Tokens live in the keychain or keystore through `react-native-keychain`, not in
AsyncStorage. The refresh token is a long-lived bearer credential, so it belongs
in protected storage.

## Deep links

The API emails links like `tripvault://verify-email?token=...`. For the OS to
hand those to the app rather than opening a browser, the scheme must be
registered natively:

- **iOS** — `Info.plist` → `CFBundleURLTypes` → `CFBundleURLSchemes`, with
  `tripvault`.
- **Android** — an `intent-filter` in `AndroidManifest.xml` carrying the scheme
  in `android:scheme`, plus `android.intent.category.BROWSABLE` and
  `android.intent.category.DEFAULT`. The `DEFAULT` category is what allows an
  app that has not been installed to still be chosen as the handler.

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
