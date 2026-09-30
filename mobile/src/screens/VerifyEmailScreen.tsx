import { useEffect, useState } from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { userFacingMessage } from '../api/errors';
import { FormError, LinkButton, PrimaryButton } from '../components/form';
import { theme } from '../theme';

interface Props {
  email: string;
  /** The token from the deep link, when the app was opened from the email. */
  token?: string;
  onResend: () => Promise<void>;
  onBackToSignIn: () => void;
}

/**
 * Shown after registration, whenever a signed-in user is not yet verified, and
 * when the app is opened cold from an emailed verification link.
 *
 * It deliberately does not say whether the account exists or the address was
 * wrong: the API answers resend requests identically either way, and echoing a
 * difference here would turn this screen into a way to test whether an address
 * has an account.
 */
export function VerifyEmailScreen({ email, token, onResend, onBackToSignIn }: Props) {
  const { verifyEmail } = useAuth();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState(false);

  // Arriving from the emailed link means there is nothing to ask the user.
  useEffect(() => {
    if (!token) {
      return;
    }
    let cancelled = false;

    setBusy(true);
    setError(null);
    verifyEmail(token)
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(userFacingMessage(err));
        }
      })
      .finally(() => {
        if (!cancelled) {
          setBusy(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [token, verifyEmail]);

  const resend = async () => {
    setError(null);
    try {
      await onResend();
      setSent(true);
    } catch (err) {
      setError(userFacingMessage(err));
    }
  };

  return (
    <View style={styles.container}>
      <Text style={styles.title}>Confirm your email</Text>
      {/* The address is only known when the user reached this screen by signing
          in or registering. Arriving straight from the emailed link on a fresh
          install there is nothing to name, and printing an empty address would
          be worse than saying nothing. */}
      <Text style={styles.body}>
        {email
          ? `We sent a link to ${email}. Open it on this device to finish setting up your account.`
          : 'Open the verification link we emailed you to finish setting up your account.'}
      </Text>

      <View style={styles.form}>
        <FormError message={error} />

        {/* Resending needs an address, so it is only offered when one is known. */}
        {email ? <PrimaryButton title="Send another email" onPress={resend} busy={busy} /> : null}
        <LinkButton title="Back to sign in" onPress={onBackToSignIn} />
      </View>

      {sent ? <Text style={styles.sent}>Sent. Check your inbox.</Text> : null}
      {busy ? <ActivityIndicator style={styles.spinner} color={theme.textMuted} /> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.background,
    justifyContent: 'center',
    padding: theme.space.lg,
  },
  title: { color: theme.text, fontSize: 26, fontWeight: '700', textAlign: 'center' },
  body: {
    color: theme.textMuted,
    fontSize: 15,
    marginBottom: theme.space.lg,
    marginTop: theme.space.md,
    textAlign: 'center',
  },
  form: { width: '100%', maxWidth: 420, alignSelf: 'center' },
  sent: { color: theme.success, marginTop: theme.space.md, textAlign: 'center' },
  spinner: { marginTop: theme.space.md },
});
