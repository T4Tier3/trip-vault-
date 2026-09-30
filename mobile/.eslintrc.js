module.exports = {
  root: true,
  extends: '@react-native',
  // The lint script was added with the rest of the tooling but no config file
  // ever landed, so `npm run lint` failed with "No files matching the pattern
  // src". A lint command that cannot run is worse than no lint command: it
  // looks like a passing check.
  ignorePatterns: ['node_modules/', 'coverage/'],
  rules: {
    // React Native 0.76 compiles JSX with the automatic runtime, so a file does
    // not have to import React to use JSX. The rule predates that and fires on
    // every element in every file, which is 95 warnings that all mean nothing.
    'react/react-in-jsx-scope': 'off',
    // `void somePromise()` is the idiom for "this is async and deliberately not
    // awaited", which is exactly what a fire-and-forget event handler wants. The
    // rule would push it towards an empty catch, which is worse.
    'no-void': 'off',
  },
};
