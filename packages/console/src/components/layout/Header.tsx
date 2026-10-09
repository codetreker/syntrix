import { Bell, Moon, Sun, User } from 'lucide-react';
import { useState } from 'react';

interface HeaderProps {
  title?: string;
  username?: string;
}

export function Header({ title, username }: HeaderProps) {
  const [isDark, setIsDark] = useState(() => {
    if (typeof window !== 'undefined') {
      return document.documentElement.classList.contains('dark');
    }
    return false;
  });

  const toggleTheme = () => {
    const newDark = !isDark;
    setIsDark(newDark);
    document.documentElement.classList.toggle('dark', newDark);
    localStorage.setItem('theme', newDark ? 'dark' : 'light');
  };

  return (
    <header className="flex items-center justify-between px-6 py-4 bg-white dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
      {/* Title */}
      <div>
        {title && <h1 className="text-xl font-semibold text-gray-900 dark:text-white">{title}</h1>}
      </div>

      {/* Actions */}
      <div className="flex items-center gap-4">
        {/* Theme Toggle */}
        <button
          onClick={toggleTheme}
          className="p-2 rounded-lg text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-gray-700 transition-colors"
          aria-label="Toggle theme"
        >
          {isDark ? <Sun className="w-5 h-5" /> : <Moon className="w-5 h-5" />}
        </button>

        {/* Notifications */}
        <button
          className="p-2 rounded-lg text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-gray-700 transition-colors relative"
          aria-label="Notifications"
        >
          <Bell className="w-5 h-5" />
          {/* Notification badge */}
          <span className="absolute top-1 right-1 w-2 h-2 bg-red-500 rounded-full"></span>
        </button>

        {/* User Menu */}
        <div className="flex items-center gap-2 pl-4 border-l border-gray-200 dark:border-gray-700">
          <div className="w-8 h-8 rounded-full bg-blue-500 flex items-center justify-center">
            <User className="w-4 h-4 text-white" />
          </div>
          {username && (
            <span className="text-sm font-medium text-gray-700 dark:text-gray-300">{username}</span>
          )}
        </div>
      </div>
    </header>
  );
}
