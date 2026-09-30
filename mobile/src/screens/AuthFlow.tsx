import { useCallback, useEffect, useState } from 'react';
import { ActivityIndicator, Linking, StatusBar, StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { parseDeepLink, type DeepLink } from '../auth/deepLink';
import { config } from '../config';
import { LinkButton } from '../components/form';
import { ForgotPasswordScreen } from './ForgotPasswordScreen';
import { LoginScreen } from './LoginScreen';
import { RegisterScreen } from './RegisterScreen';
import { ResetPasswordScreen } from './ResetPasswordScreen';
import { SignedInScreen } from './SignedInScreen';
import { VerifyEmailScreen } from './VerifyEmailScreen';
import { theme } from '../theme';

type Screen = 'login' | 'register' | 'verify' | 'forgot' | 'reset';

/**
 * Chooses what to show from the session state.
 *
 * There is no navigation library here on purpose: the auth journey is a handful
 * of states, and modelling them explicitly is easier to follow than a stack
 * that can get out of step with the session. Trip and album navigation will
 * need a real navigator and will replace this file.
 */
export function AuthFlow() {
  const { state, resendVerification, retry } = useAuth();
  const [screen, setScreen] = useState<Screen>('login');
  const [pendingEmail, setPendingEmail] = useState('');
  const [link, setLink] = useState<DeepLink>({ screen: 'unknown', token: null });

  const handleUrl = useCallback((url: string) => {
    const next = parseDeepLink(url, config.linkScheme);
    setLink(next);
    // A link is navigation, not just data. Switching the screen here is what
    // makes a cold start work: the app can be launched by the link with no
    // prior state, so a screen that only reads the link on a later render
    // would never open.
    if (next.screen === 'verify-email') {
      setScreen('verify');
    } else if (next.screen === 'reset-password') {
      setScreen('reset');
    }
  }, []);

  // Leaving a link-driven screen forgets the link. Without this, finishing a
  // reset and returning to sign in would re-open the reset screen, because the
  // token would still be sitting in state.
  const leaveScreen = useCallback(() => {
    setLink({ screen: 'unknown', token: null });
    setScreen('login');
  }, []);

  // A cold start from a link, and a link arriving while the app is open.
  useEffect(() => {
    void Linking.getInitialURL().then((url) => {
      if (url) {
        handleUrl(url);
      }
    });
    const subscription = Linking.addEventListener('url', (event) => handleUrl(event.url));
    return () => subscription.remove();
  }, [handleUrl]);

  if (state.status === 'loading') {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color={theme.accent} />
      </View>
    );
  }

  if (state.status === 'error') {
    return (
      <View style={styles.center}>
        <Text style={styles.errorTitle}>Cannot reach TripVault</Text>
        <Text style={styles.errorBody}>{state.message}</Text>
        <LinkButton title="Try again" onPress={() => void retry()} />
      </View>
    );
  }

  if (state.status === 'unverified') {
    return (
      <VerifyEmailScreen
        email={state.user.email}
        {...(link.screen === 'verify-email' ? { token: link.token } : {})}
        onResend={() => resendVerification(state.user.email)}
        onBackToSignIn={leaveScreen}
      />
    );
  }

  if (state.status === 'authenticated') {
    return <SignedInScreen />;
  }

  if (screen === 'register') {
    return (
      <RegisterScreen
        onBackToSignIn={() => setScreen('login')}
        onRegistered={(email) => {
          setPendingEmail(email);
          setScreen('verify');
        }}
      />
    );
  }

  if (screen === 'verify') {
    return (
      <VerifyEmailScreen
        email={pendingEmail}
        {...(link.screen === 'verify-email' ? { token: link.token } : {})}
        onResend={() => resendVerification(pendingEmail)}
        onBackToSignIn={leaveScreen}
      />
    );
  }

  if (screen === 'forgot') {
    return <ForgotPasswordScreen onBackToSignIn={() => setScreen('login')} />;
  }

  if (screen === 'reset') {
    return (
      <ResetPasswordScreen
        {...(link.screen === 'reset-password' ? { token: link.token } : {})}
        onDone={leaveScreen}
      />
    );
  }

  return (
    <LoginScreen
      onRegister={() => setScreen('register')}
      onForgotPassword={() => setScreen('forgot')}
    />
  );
}

const styles = StyleSheet.create({
  center: {
    alignItems: 'center',
    backgroundColor: theme.background,
    flex: 1,
    justifyContent: 'center',
    padding: theme.space.lg,
  },
  errorTitle: { color: theme.text, fontSize: 20, fontWeight: '600' },
  errorBody: {
    color: theme.textMuted,
    marginBottom: theme.space.lg,
    marginTop: theme.space.sm,
    textAlign: 'center',
  },
});

export function Root() {
  return (
    <>
      <StatusBar barStyle="light-content" backgroundColor={theme.background} />
      <AuthFlow />
    </>
  );
}
