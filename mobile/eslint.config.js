// Expo SDK 57 and ESLint 9 both use flat config, so the old .eslintrc.js shape
// no longer applies. `eslint-config-expo/flat` is the config Expo's own template
// ships, and it already knows about React 19 and the automatic JSX runtime.
const expoConfig = require('eslint-config-expo/flat');

module.exports = [
  ...expoConfig,
  {
    ignores: ['node_modules/**', 'coverage/**', '.expo/**', 'dist/**', 'android/**', 'ios/**'],
  },
  {
    rules: {
      // `void somePromise()` is the idiom for "this is async and deliberately not
      // awaited", which is exactly what a fire-and-forget event handler wants. The
      // rule would push it towards an empty catch, which is worse.
      'no-void': 'off',
    },
  },
];
