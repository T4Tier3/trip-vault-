// Learn more: https://docs.expo.dev/guides/monorepos
const { getDefaultConfig } = require('expo/metro-config');

/**
 * Metro configuration.
 *
 * Metro has to be told to watch the repository root as well as this directory,
 * otherwise a change to the backend is not picked up by a fast refresh and the
 * developer is left wondering why the app is talking to old behaviour.
 */
const path = require('path');

const projectRoot = __dirname;
const workspaceRoot = path.resolve(projectRoot, '..');

/** @type {import('expo/metro-config').MetroConfig} */
const config = {
  // Packages installed at the repository root resolve from there.
  watchFolders: [workspaceRoot],
  resolver: {
    nodeModulesPaths: [
      path.resolve(projectRoot, 'node_modules'),
      path.resolve(workspaceRoot, 'node_modules'),
    ],
  },
};

module.exports = getDefaultConfig(projectRoot, config);
