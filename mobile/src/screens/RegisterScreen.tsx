import { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { checkPassword } from '../auth/passwordPolicy';
import { isApiError, userFacingMessage } from '../api/errors';
import { Field, FormError, LinkButton, PrimaryButton } from '../components/form';
import { theme } from '../theme';

/**
 * Mirrors the server's password rules so the user is told what to fix before a
 * round trip.
 *
 * The rules themselves live in auth/passwordPolicy so that the registration and
 * password-reset forms cannot drift apart. The server remains the authority and
 * re-checks everything.
 */

interface Props {
  onBackToSignIn: () => void;
  onRegistered: (email: string) => void;
}

export function RegisterScreen({ onBackToSignIn, onRegistered }: Props) {
  const { register } = useAuth();
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  const submit = async () => {
    const localPasswordError = checkPassword(password);
    if (localPasswordError) {
      setFieldErrors({ password: localPasswordError });
      return;
    }

    setBusy(true);
    setFormError(null);
    setFieldErrors({});
    try {
      await register({ email: email.trim(), password, name: name.trim() });
      onRegistered(email.trim());
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
        <Text style={styles.title}>Create your account</Text>
        <Text style={styles.subtitle}>
          We will email you a link to confirm your address.
        </Text>

        <View style={styles.form}>
          <FormError message={formError} />

          <Field
            label="Name"
            value={name}
            onChangeText={(v) => {
              setName(v);
              clearError();
            }}
            placeholder="Saurabh"
            textContentType="name"
            autoComplete="name"
            error={fieldErrors.name}
            editable={!busy}
          />

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
            autoComplete="new-password"
            error={fieldErrors.password ?? (checkPassword(password) || undefined)}
            editable={!busy}
          />

          <PrimaryButton title="Create account" onPress={submit} busy={busy} />

          <LinkButton title="I already have an account" onPress={onBackToSignIn} />
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1, backgroundColor: theme.background },
  container: { flexGrow: 1, justifyContent: 'center', padding: theme.space.lg },
  title: { color: theme.text, fontSize: 26, fontWeight: '700', textAlign: 'center' },
  subtitle: {
    color: theme.textMuted,
    fontSize: 15,
    marginBottom: theme.space.lg,
    marginTop: theme.space.sm,
    textAlign: 'center',
  },
  form: { width: '100%', maxWidth: 420, alignSelf: 'center' },
});
