import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { Database } from 'lucide-react';
import { Button, Input } from '../../components/ui';
import { useAuthStore } from '../../stores';

export default function LoginPage() {
  const navigate = useNavigate();
  const { login, isLoading, error, clearError } = useAuthStore();
  
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [localError, setLocalError] = useState('');

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setLocalError('');
    clearError();

    if (!username.trim() || !password.trim()) {
      setLocalError('Please enter username and password');
      return;
    }

    try {
      await login({ username, password });
      navigate('/');
    } catch (err: unknown) {
      // Error is already set in store, but we can add more specific handling
      if (err instanceof Error) {
        setLocalError(err.message || 'Login failed. Please check your credentials.');
      } else {
        setLocalError('Login failed. Please check your credentials.');
      }
    }
  };

  const displayError = localError || error;

  return (
    <div className="bg-white dark:bg-gray-800 rounded-xl shadow-lg p-8">
      {/* Header */}
      <div className="text-center mb-8">
        <div className="inline-flex items-center justify-center w-16 h-16 rounded-full bg-blue-100 dark:bg-blue-900 mb-4">
          <Database className="w-8 h-8 text-blue-600 dark:text-blue-400" />
        </div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">Syntrix Console</h1>
        <p className="text-gray-500 dark:text-gray-400 mt-2">Sign in to your account</p>
      </div>

      {/* Error Message */}
      {displayError && (
        <div className="mb-6 p-4 bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800 rounded-lg">
          <p className="text-sm text-red-600 dark:text-red-400">{displayError}</p>
        </div>
      )}

      {/* Form */}
      <form onSubmit={handleSubmit} className="space-y-6">
        <Input
          label="Username"
          type="text"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          placeholder="Enter your username"
          autoComplete="username"
          disabled={isLoading}
        />

        <Input
          label="Password"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Enter your password"
          autoComplete="current-password"
          disabled={isLoading}
        />

        <Button
          type="submit"
          variant="primary"
          className="w-full"
          loading={isLoading}
        >
          Sign In
        </Button>
      </form>

      {/* Footer */}
      <div className="mt-6 text-center">
        <p className="text-sm text-gray-500 dark:text-gray-400">
          Real-time Database Management Console
        </p>
      </div>
    </div>
  );
}
