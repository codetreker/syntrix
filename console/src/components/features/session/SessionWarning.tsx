import { useState, useEffect } from 'react'
import { useAuthStore } from '../../../stores'
import { Clock, RefreshCw, LogOut } from 'lucide-react'
import { Button } from '../../ui/Button'

export function SessionWarning() {
  const [visible, setVisible] = useState(false)
  const [expiresIn, setExpiresIn] = useState(0)
  const { refreshSession, logout, getTimeUntilExpiry, isAuthenticated } = useAuthStore()

  useEffect(() => {
    // Listen for session expiring event
    const handleSessionExpiring = (event: CustomEvent<{ expiresIn: number }>) => {
      setExpiresIn(event.detail.expiresIn)
      setVisible(true)
    }

    window.addEventListener('session-expiring', handleSessionExpiring as EventListener)

    return () => {
      window.removeEventListener('session-expiring', handleSessionExpiring as EventListener)
    }
  }, [])

  // Update countdown
  useEffect(() => {
    if (!visible || !isAuthenticated) return

    const interval = setInterval(() => {
      const remaining = getTimeUntilExpiry()
      if (remaining <= 0) {
        setVisible(false)
        logout()
      } else {
        setExpiresIn(remaining)
      }
    }, 1000)

    return () => clearInterval(interval)
  }, [visible, isAuthenticated, getTimeUntilExpiry, logout])

  const handleRefresh = async () => {
    try {
      await refreshSession()
      setVisible(false)
    } catch {
      // Refresh failed, will be logged out automatically
    }
  }

  const handleLogout = () => {
    setVisible(false)
    logout()
    window.location.href = '/console/login'
  }

  const formatTime = (seconds: number) => {
    const mins = Math.floor(seconds / 60)
    const secs = seconds % 60
    return `${mins}:${secs.toString().padStart(2, '0')}`
  }

  if (!visible || !isAuthenticated) return null

  return (
    <div className="fixed top-4 right-4 z-50 animate-in slide-in-from-top-2 fade-in duration-300">
      <div className="bg-amber-50 dark:bg-amber-900/90 border border-amber-200 dark:border-amber-700 rounded-lg shadow-lg p-4 max-w-sm">
        <div className="flex items-start gap-3">
          <div className="p-2 bg-amber-100 dark:bg-amber-800 rounded-full">
            <Clock className="w-5 h-5 text-amber-600 dark:text-amber-300" />
          </div>
          <div className="flex-1">
            <h4 className="font-medium text-amber-800 dark:text-amber-200">
              Session Expiring Soon
            </h4>
            <p className="text-sm text-amber-700 dark:text-amber-300 mt-1">
              Your session will expire in{' '}
              <span className="font-mono font-bold">{formatTime(expiresIn)}</span>
            </p>
            <div className="flex gap-2 mt-3">
              <Button
                size="sm"
                variant="primary"
                onClick={handleRefresh}
              >
                <RefreshCw className="w-4 h-4 mr-1" />
                Extend
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onClick={handleLogout}
              >
                <LogOut className="w-4 h-4 mr-1" />
                Logout
              </Button>
            </div>
          </div>
          <button
            onClick={() => setVisible(false)}
            className="text-amber-500 hover:text-amber-700 dark:text-amber-400 dark:hover:text-amber-200"
          >
            ×
          </button>
        </div>
      </div>
    </div>
  )
}
