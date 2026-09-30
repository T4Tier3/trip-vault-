import { AuthProvider } from './src/auth/AuthProvider';
import { Root } from './src/screens/AuthFlow';

export default function App() {
  return (
    <AuthProvider>
      <Root />
    </AuthProvider>
  );
}
