import { useState, type ReactNode } from 'react';
import { Sidebar } from './Sidebar';
import { Header } from './Header';
import { Menu } from 'lucide-react';

interface LayoutProps {
  children: ReactNode;
  title?: string;
  username?: string;
  isAdmin?: boolean;
  onLogout?: () => void;
}

export function Layout({ children, title, username, isAdmin, onLogout }: LayoutProps) {
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  return (
    <div className="flex h-screen bg-gray-100 dark:bg-gray-900">
      {/* Sidebar - includes both mobile and desktop versions */}
      <Sidebar 
        isAdmin={isAdmin} 
        onLogout={onLogout}
        isMobileOpen={mobileMenuOpen}
        onMobileClose={() => setMobileMenuOpen(false)}
      />

      {/* Main Content */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Mobile Header with hamburger */}
        <div className="md:hidden flex items-center justify-between p-4 bg-white dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
          <button
            onClick={() => setMobileMenuOpen(true)}
            className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
            aria-label="Open menu"
          >
            <Menu className="w-6 h-6 text-gray-600 dark:text-gray-300" />
          </button>
          <span className="font-semibold text-gray-900 dark:text-white">Syntrix</span>
          <div className="w-10" /> {/* Spacer for centering */}
        </div>

        {/* Desktop Header */}
        <div className="hidden md:block">
          <Header title={title} username={username} />
        </div>

        {/* Page Content */}
        <main className="flex-1 overflow-auto p-4 md:p-6">
          {children}
        </main>
      </div>
    </div>
  );
}

// Auth layout for login/signup pages
interface AuthLayoutProps {
  children: ReactNode;
}

export function AuthLayout({ children }: AuthLayoutProps) {
  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-100 dark:bg-gray-900 p-4">
      <div className="w-full max-w-md">
        {children}
      </div>
    </div>
  );
}
