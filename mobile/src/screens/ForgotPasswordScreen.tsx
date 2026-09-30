import { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { userFacingMessage } from '../api/errors';
import { Field, FormError, LinkButton, PrimaryButton } from '../components/form';
import { theme } from '../theme';

interface Props {
  onBackToSignIn: () => void;
}

/**
 * Asks the server to send a reset link.
 *
 * The confirmation text is written to be equally true whether or not the address
 * has an account. The API answers identically either way, so showing something
 * more specific here would turn this form into a way to discover which addresses
 * are registered.
 */
export function ForgotPasswordScreen({ onBackToSignIn }: Props) {
  const { requestPasswordReset } = useAuth();
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [sent, setSent] = useState(false);

  const submit = async () => {
    if (email.trim() === '') {
      setFormError('Enter the email address on your account.');
      return;
    }

    setBusy(true);
    setFormError(null);
    try {
      await requestPasswordReset(email.trim());
      setSent(true);
    } catch (err) {
      setFormError(userFacingMessage(err));
    } finally {
      setBusy(false);
    }
  };

  if (sent) {
    return (
      <View style={styles.container}>
        <Text style={styles.title}>Check your email</Text>
        <Text style={styles.body}>
          If an account exists for that address, a link to set a new password is
          on its way. The link is good for an hour.
        </Text>
        <LinkButton title="Back to sign in" onPress={onBackToSignIn} />
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <Text style={styles.title}>Reset your password</Text>
        <Text style={styles.subtitle}>
          We will email you a link to choose a new one.
        </Text>

        <View style={styles.form}>
          <FormError message={formError} />

          <Field
            label="Email"
            value={email}
            onChangeText={(v) => {
              setEmail(v);
              setFormError(null);
            }}
            placeholder="you@example.com"
            keyboardType="email-address"
            autoCapitalize="none"
            textContentType="emailAddress"
            autoComplete="email"
            editable={!busy}
          />

          <PrimaryButton title="Send reset link" onPress={submit} busy={busy} />
          <LinkButton title="Back to sign in" onPress={onBackToSignIn} />
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1, backgroundColor: theme.background },
  container: {
    flexGrow: 1,
    justifyContent: 'center',
    padding: theme.space.lg,
  },
  title: { color: theme.text, fontSize: 26, fontWeight: '700', textAlign: 'center' },
  subtitle: {
    color: theme.textMuted,
    fontSize: 15,
    marginBottom: theme.space.lg,
    marginTop: theme.space.sm,
    textAlign: 'center',
  },
  body: {
    color: theme.textMuted,
    fontSize: 15,
    marginBottom: theme.space.lg,
    marginTop: theme.space.md,
    textAlign: 'center',
  },
  form: { width: '100%', maxWidth: 420, alignSelf: 'center' },
});
