/**
 * The JavaScript entry point.
 *
 * React Native starts the app by asking the registry for a component registered
 * under the name in app.json, so this file is what makes App.tsx reachable. It
 * is the one file the native projects need to be able to see, and it is source
 * rather than generated output, which is why it lives here instead of being
 * created by `react-native init` on each machine.
 */
import { AppRegistry } from 'react-native';

import App from './App';
import { name as appName } from './app.json';

AppRegistry.registerComponent(appName, () => App);
