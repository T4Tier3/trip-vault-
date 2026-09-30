import { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { isApiError, userFacingMessage } from '../api/errors';
import { Field, FormError, LinkButton, PrimaryButton } from '../components/form';
import { theme } from '../theme';

interface Props {
  onRegister: () => void;
  onForgotPassword: () => void;
}

/**
 * The sign-in screen.
 *
 * Two details worth noting. The password field is submitted from the keyboard's
 * Go key, because on a phone that is the only way a user will submit the form.
 * And the error banner is cleared as soon as the user edits either field, so a
 * stale "wrong password" does not sit there contradicting what they are typing.
 */
export function LoginScreen({ onRegister, onForgotPassword }: Props) {
  const { signIn } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  const submit = async () => {
    setBusy(true);
    setFormError(null);
    setFieldErrors({});
    try {
      await signIn(email.trim(), password);
    } catch (err) {
      // A validation failure names the fields, so those go under the inputs.
      // Everything else is one problem with the form as a whole.
      if (isApiError(err) && Object.keys(err.fields).length > 0) {
        setFieldErrors(err.fields);
      } else {
        setFormError(userFacingMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const clearError = () => {
    setFormError(null);
    setFieldErrors({});
  };

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <Text style={styles.title}>TripVault</Text>
        <Text style={styles.subtitle}>Your trips, kept safe.</Text>

        <View style={styles.form}>
          <FormError message={formError} />

          <Field
            label="Email"
            value={email}
            onChangeText={(v) => {
              setEmail(v);
              clearError();
            }}
            placeholder="you@example.com"
            keyboardType="email-address"
            autoCapitalize="none"
            textContentType="emailAddress"
            autoComplete="email"
            error={fieldErrors.email}
            editable={!busy}
          />

          <Field
            label="Password"
            value={password}
            onChangeText={(v) => {
              setPassword(v);
              clearError();
            }}
            secureTextEntry
            textContentType="password"
            autoComplete="current-password"
            error={fieldErrors.password}
            editable={!busy}
          />

          <PrimaryButton title="Sign in" onPress={submit} busy={busy} />

          <LinkButton title="Create an account" onPress={onRegister} />
          <LinkButton title="Forgot your password?" onPress={onForgotPassword} />
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1, backgroundColor: theme.background },
  container: { flexGrow: 1, justifyContent: 'center', padding: theme.space.lg },
  title: { color: theme.text, fontSize: 32, fontWeight: '700', textAlign: 'center' },
  subtitle: {
    color: theme.textMuted,
    fontSize: 15,
    marginBottom: theme.space.lg,
    marginTop: theme.space.sm,
    textAlign: 'center',
  },
  form: { width: '100%', maxWidth: 420, alignSelf: 'center' },
});
