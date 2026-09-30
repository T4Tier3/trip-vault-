import { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { checkPassword } from '../auth/passwordPolicy';
import { isApiError, userFacingMessage } from '../api/errors';
import { Field, FormError, LinkButton, PrimaryButton } from '../components/form';
import { theme } from '../theme';

interface Props {
  /** The token from the emailed link. Absent when the link was truncated. */
  token?: string;
  onDone: () => void;
}

/**
 * Chooses a new password from a reset link.
 *
 * Two things happen on success that the user needs telling about. Every other
 * session is signed out, because a password reset is also the remedy for a
 * stolen account, so a device that still had the old session should not keep
 * it. And the app does not sign in automatically: the reset flow had no
 * password to sign in with, and inventing a new one silently would skip the
 * sign-in the user actually needs.
 */
export function ResetPasswordScreen({ token, onDone }: Props) {
  const { resetPassword } = useAuth();
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string | undefined>>({});

  if (!token) {
    return (
      <View style={styles.container}>
        <Text style={styles.title}>That link did not work</Text>
        <Text style={styles.body}>
          The reset link is missing its code. It may have been cut off when it
          was copied. Ask for a new one from the sign-in screen.
        </Text>
        <LinkButton title="Back to sign in" onPress={onDone} />
      </View>
    );
  }

  const submit = async () => {
    const localError = checkPassword(password);
    if (localError) {
      setFieldErrors({ password: localError });
      return;
    }
    // Checked here so a mismatch is an instant, obvious message rather than a
    // server round trip that accepts a password the user did not mean to set.
    if (password !== confirm) {
      setFieldErrors({ confirm: 'The two passwords do not match.' });
      return;
    }

    setBusy(true);
    setFormError(null);
    setFieldErrors({});
    try {
      await resetPassword(token, password);
      onDone();
    } catch (err) {
      if (isApiError(err) && Object.keys(err.fields).length > 0) {
        setFieldErrors(err.fields);
      } else {
        setFormError(userFacingMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const clearError = (field: 'password' | 'confirm') => {
    setFormError(null);
    // A fresh object rather than a delete, so React sees the change.
    setFieldErrors((current) => ({ ...current, [field]: undefined }));
  };

  const passwordError = fieldErrors.password ?? checkPassword(password);

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <Text style={styles.title}>Choose a new password</Text>
        <Text style={styles.subtitle}>
          Setting a new password signs out your other devices.
        </Text>

        <View style={styles.form}>
          <FormError message={formError} />

          <Field
            label="New password"
            value={password}
            onChangeText={(v) => {
              setPassword(v);
              clearError('password');
            }}
            secureTextEntry
            textContentType="password"
            autoComplete="new-password"
            error={passwordError}
            editable={!busy}
          />

          <Field
            label="Confirm new password"
            value={confirm}
            onChangeText={(v) => {
              setConfirm(v);
              clearError('confirm');
            }}
            secureTextEntry
            textContentType="password"
            autoComplete="new-password"
            error={fieldErrors.confirm}
            editable={!busy}
          />

          <PrimaryButton title="Set new password" onPress={submit} busy={busy} />
          <LinkButton title="Back to sign in" onPress={onDone} />
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
