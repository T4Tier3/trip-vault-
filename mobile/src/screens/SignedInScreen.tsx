import { useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { useAuth } from '../auth/AuthProvider';
import { userFacingMessage } from '../api/errors';
import { FormError, PrimaryButton } from '../components/form';
import { theme } from '../theme';

/**
 * The signed-in placeholder.
 *
 * Trips and albums arrive in the next phase. What is here now is the proof that
 * the session round trip works end to end, plus the one action that cannot be
 * deferred: signing out. An app that can sign a user in but offers no way back
 * out is not shippable, so this earns its place even though the rest of the
 * signed-in experience is still missing.
 */
export function SignedInScreen() {
  const { state, signOut } = useAuth();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The session state narrows this to authenticated, so the user is present.
  const user = state.status === 'authenticated' ? state.user : null;

  const onSignOut = async () => {
    setBusy(true);
    setError(null);
    try {
      // The client clears the local session even if the server call fails, so a
      // failure here is not a reason to keep someone signed in. The error is
      // shown anyway in case it does not clear, but it must not block the way out.
      await signOut();
    } catch (err) {
      setError(userFacingMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <View style={styles.container}>
      <Text style={styles.title}>Signed in</Text>
      <Text style={styles.name}>{user?.name}</Text>
      <Text style={styles.email}>{user?.email}</Text>

      <View style={styles.form}>
        <FormError message={error} />
        <PrimaryButton title="Sign out" onPress={onSignOut} busy={busy} />
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    alignItems: 'center',
    backgroundColor: theme.background,
    flex: 1,
    justifyContent: 'center',
    padding: theme.space.lg,
  },
  title: { color: theme.text, fontSize: 22, fontWeight: '600' },
  name: { color: theme.text, fontSize: 17, marginTop: theme.space.md },
  email: { color: theme.textMuted, marginTop: 4 },
  form: { marginTop: theme.space.lg, width: '100%', maxWidth: 420 },
});
