/**
 * Babel configuration.
 *
 * Expo owns this. `babel-preset-expo` applies the JSX runtime, the inline
 * requires and the module resolution Metro expects, and it is what the SDK's
 * own release process is tested against. Swapping in a bare React Native preset
 * works until it does not, because Expo's Babel plugins run ahead of it.
 */
module.exports = function babelConfig(api) {
  api.cache(true);
  return {
    presets: ['babel-preset-expo'],
  };
};
