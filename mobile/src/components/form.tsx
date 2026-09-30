import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';

import { theme } from '../theme';

interface FieldProps {
  label: string;
  value: string;
  onChangeText: (value: string) => void;
  placeholder?: string;
  error?: string;
  secureTextEntry?: boolean;
  autoCapitalize?: 'none' | 'words' | 'sentences';
  keyboardType?: 'default' | 'email-address';
  textContentType?: 'username' | 'password' | 'emailAddress' | 'name' | 'oneTimeCode';
  autoComplete?: 'email' | 'current-password' | 'new-password' | 'name' | 'one-time-code';
  editable?: boolean;
}

/**
 * One labelled input, with its error message beneath it.
 *
 * The error is tied to the input with accessibilityLabel so a screen reader
 * announces the problem with the field rather than as an unrelated stray text.
 */
export function Field({
  label,
  value,
  onChangeText,
  placeholder,
  error,
  secureTextEntry,
  autoCapitalize = 'sentences',
  keyboardType = 'default',
  textContentType,
  autoComplete,
  editable = true,
}: FieldProps) {
  return (
    <View style={styles.field}>
      <Text style={styles.label}>{label}</Text>
      <TextInput
        style={[styles.input, error ? styles.inputError : null]}
        value={value}
        onChangeText={onChangeText}
        placeholder={placeholder}
        placeholderTextColor={theme.textMuted}
        secureTextEntry={secureTextEntry}
        autoCapitalize={autoCapitalize}
        autoCorrect={false}
        keyboardType={keyboardType}
        textContentType={textContentType}
        autoComplete={autoComplete}
        editable={editable}
        accessibilityLabel={label}
        accessibilityHint={error}
      />
      {error ? (
        <Text style={styles.error} accessibilityLiveRegion="polite">
          {error}
        </Text>
      ) : null}
    </View>
  );
}

interface PrimaryButtonProps {
  title: string;
  onPress: () => void;
  busy?: boolean;
  disabled?: boolean;
}

export function PrimaryButton({ title, onPress, busy = false, disabled = false }: PrimaryButtonProps) {
  const inactive = busy || disabled;
  return (
    <Pressable
      style={({ pressed }) => [styles.button, inactive ? styles.buttonDisabled : null, pressed && !inactive ? styles.buttonPressed : null]}
      onPress={onPress}
      disabled={inactive}
      accessibilityRole="button"
      accessibilityState={{ disabled: inactive, busy }}
    >
      {busy ? <ActivityIndicator color={theme.background} /> : <Text style={styles.buttonText}>{title}</Text>}
    </Pressable>
  );
}

export function LinkButton({ title, onPress }: { title: string; onPress: () => void }) {
  return (
    <Pressable onPress={onPress} accessibilityRole="button" style={styles.link}>
      <Text style={styles.linkText}>{title}</Text>
    </Pressable>
  );
}

/** A banner for a whole-form failure, such as a wrong password. */
export function FormError({ message }: { message: string | null }) {
  if (!message) {
    return null;
  }
  return (
    <View style={styles.banner} accessibilityLiveRegion="assertive">
      <Text style={styles.bannerText}>{message}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  field: { marginBottom: theme.space.md },
  label: { color: theme.text, fontSize: 14, marginBottom: 6 },
  input: {
    backgroundColor: theme.surface,
    borderColor: theme.border,
    borderRadius: 10,
    borderWidth: 1,
    color: theme.text,
    fontSize: 16,
    paddingHorizontal: theme.space.md,
    paddingVertical: 12,
  },
  inputError: { borderColor: theme.danger },
  error: { color: theme.danger, fontSize: 13, marginTop: 4 },
  button: {
    backgroundColor: theme.accent,
    borderRadius: 10,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 48,
    paddingVertical: 12,
  },
  buttonPressed: { opacity: 0.85 },
  buttonDisabled: { backgroundColor: theme.border },
  buttonText: { color: theme.background, fontSize: 16, fontWeight: '600' },
  link: { alignItems: 'center', paddingVertical: 12 },
  linkText: { color: theme.accent, fontSize: 15, fontWeight: '500' },
  banner: {
    backgroundColor: theme.dangerSurface,
    borderRadius: 10,
    marginBottom: theme.space.md,
    padding: theme.space.md,
  },
  bannerText: { color: theme.danger, fontSize: 14 },
});
