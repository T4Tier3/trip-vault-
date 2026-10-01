/**
 * The JavaScript entry point.
 *
 * `registerRootComponent` is Expo's equivalent of `AppRegistry.registerComponent`
 * and it also handles the web bundle and the fast-refresh wiring, so Expo owns
 * this file rather than the native projects.
 */
import { registerRootComponent } from 'expo';

import App from './App';

registerRootComponent(App);
