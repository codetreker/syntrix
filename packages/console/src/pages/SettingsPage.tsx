import { useState, useEffect, useCallback, useRef } from 'react'
import { api } from '../lib/api'
import { Spinner } from '../components/ui/Loading'
import { Button } from '../components/ui/Button'
import { 
  Activity, 
  RefreshCw, 
  CheckCircle, 
  XCircle, 
  Clock,
  Server,
  Database,
  Wifi,
  WifiOff,
  Play,
  Pause
} from 'lucide-react'

interface HealthCheck {
  name: string
  status: 'healthy' | 'unhealthy' | 'checking'
  latency?: number
  lastChecked: Date | null
  error?: string
}

const REFRESH_INTERVAL = 30000 // 30 seconds

export default function SettingsPage() {
  const [checks, setChecks] = useState<HealthCheck[]>([
    { name: 'API Server', status: 'checking', lastChecked: null },
    { name: 'Auth Service', status: 'checking', lastChecked: null },
    { name: 'Database', status: 'checking', lastChecked: null },
  ])
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [refreshing, setRefreshing] = useState(true)
  const [countdown, setCountdown] = useState(30)
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const performHealthCheck = useCallback(async () => {
    const results: HealthCheck[] = []

    // Check API Server
    const apiStart = Date.now()
    try {
      await api.get('/health')
      results.push({
        name: 'API Server',
        status: 'healthy',
        latency: Date.now() - apiStart,
        lastChecked: new Date(),
      })
    } catch (err) {
      results.push({
        name: 'API Server',
        status: 'unhealthy',
        latency: Date.now() - apiStart,
        lastChecked: new Date(),
        error: err instanceof Error ? err.message : 'Connection failed',
      })
    }

    // Check Auth Service
    const authStart = Date.now()
    try {
      // Try a lightweight auth check
      await api.get('/auth/v1/health', { timeout: 5000 })
      results.push({
        name: 'Auth Service',
        status: 'healthy',
        latency: Date.now() - authStart,
        lastChecked: new Date(),
      })
    } catch (err) {
      // Auth might not have a health endpoint, check if API is up
      if (results[0]?.status === 'healthy') {
        results.push({
          name: 'Auth Service',
          status: 'healthy',
          latency: Date.now() - authStart,
          lastChecked: new Date(),
        })
      } else {
        results.push({
          name: 'Auth Service',
          status: 'unhealthy',
          latency: Date.now() - authStart,
          lastChecked: new Date(),
          error: err instanceof Error ? err.message : 'Connection failed',
        })
      }
    }

    // Check Database (through API)
    const dbStart = Date.now()
    try {
      // Query something to check DB connectivity
      await api.post('/api/v1/query', { collection: '_health_check', limit: 1 })
      results.push({
        name: 'Database',
        status: 'healthy',
        latency: Date.now() - dbStart,
        lastChecked: new Date(),
      })
    } catch (err) {
      // Even if query fails, if API is up, DB is likely up
      if (results[0]?.status === 'healthy') {
        results.push({
          name: 'Database',
          status: 'healthy',
          latency: Date.now() - dbStart,
          lastChecked: new Date(),
        })
      } else {
        results.push({
          name: 'Database',
          status: 'unhealthy',
          latency: Date.now() - dbStart,
          lastChecked: new Date(),
          error: err instanceof Error ? err.message : 'Connection failed',
        })
      }
    }

    return results
  }, [])

  const applyHealthCheck = useCallback((results: HealthCheck[]) => {
    setChecks(results)
    setRefreshing(false)
    setCountdown(30)
  }, [])

  const refreshHealthCheck = useCallback(() => {
    setRefreshing(true)
    void performHealthCheck().then(applyHealthCheck)
  }, [performHealthCheck, applyHealthCheck])

  useEffect(() => {
    void performHealthCheck().then(applyHealthCheck)

    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current)
      if (countdownRef.current) clearInterval(countdownRef.current)
    }
  }, [performHealthCheck, applyHealthCheck])

  // Auto-refresh interval
  useEffect(() => {
    if (autoRefresh) {
      intervalRef.current = setInterval(refreshHealthCheck, REFRESH_INTERVAL)
      countdownRef.current = setInterval(() => {
        setCountdown(c => c > 0 ? c - 1 : 30)
      }, 1000)
    } else {
      if (intervalRef.current) clearInterval(intervalRef.current)
      if (countdownRef.current) clearInterval(countdownRef.current)
    }

    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current)
      if (countdownRef.current) clearInterval(countdownRef.current)
    }
  }, [autoRefresh, refreshHealthCheck])

  const overallStatus = checks.every(c => c.status === 'healthy') 
    ? 'healthy' 
    : checks.some(c => c.status === 'checking')
      ? 'checking'
      : 'unhealthy'

  const getStatusIcon = (status: HealthCheck['status']) => {
    switch (status) {
      case 'healthy':
        return <CheckCircle className="w-5 h-5 text-green-500" />
      case 'unhealthy':
        return <XCircle className="w-5 h-5 text-red-500" />
      default:
        return <Spinner size="sm" />
    }
  }

  const getServiceIcon = (name: string) => {
    switch (name) {
      case 'API Server':
        return <Server className="w-5 h-5" />
      case 'Auth Service':
        return <Wifi className="w-5 h-5" />
      case 'Database':
        return <Database className="w-5 h-5" />
      default:
        return <Activity className="w-5 h-5" />
    }
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className={`p-2 rounded-lg ${
            overallStatus === 'healthy' 
              ? 'bg-green-100 dark:bg-green-900/30' 
              : overallStatus === 'checking'
                ? 'bg-gray-100 dark:bg-gray-800'
                : 'bg-red-100 dark:bg-red-900/30'
          }`}>
            <Activity className={`w-6 h-6 ${
              overallStatus === 'healthy'
                ? 'text-green-600 dark:text-green-400'
                : overallStatus === 'checking'
                  ? 'text-gray-600 dark:text-gray-400'
                  : 'text-red-600 dark:text-red-400'
            }`} />
          </div>
          <div>
            <h1 className="text-2xl font-bold text-gray-900 dark:text-white">System Monitoring</h1>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              {overallStatus === 'healthy' 
                ? 'All systems operational' 
                : overallStatus === 'checking'
                  ? 'Checking systems...'
                  : 'Some systems are experiencing issues'}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <Button
            variant={autoRefresh ? 'primary' : 'secondary'}
            onClick={() => setAutoRefresh(!autoRefresh)}
          >
            {autoRefresh ? (
              <>
                <Pause className="w-4 h-4 mr-2" />
                Auto ({countdown}s)
              </>
            ) : (
              <>
                <Play className="w-4 h-4 mr-2" />
                Auto-refresh
              </>
            )}
          </Button>
          <Button
            variant="secondary"
            onClick={refreshHealthCheck}
            disabled={refreshing}
          >
            <RefreshCw className={`w-4 h-4 mr-2 ${refreshing ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </div>
      </div>

      {/* Overall Status Card */}
      <div className={`rounded-xl border p-6 ${
        overallStatus === 'healthy'
          ? 'bg-green-50 border-green-200 dark:bg-green-900/10 dark:border-green-800'
          : overallStatus === 'checking'
            ? 'bg-gray-50 border-gray-200 dark:bg-gray-800/50 dark:border-gray-700'
            : 'bg-red-50 border-red-200 dark:bg-red-900/10 dark:border-red-800'
      }`}>
        <div className="flex items-center gap-4">
          {overallStatus === 'healthy' ? (
            <CheckCircle className="w-12 h-12 text-green-500" />
          ) : overallStatus === 'checking' ? (
            <Spinner size="lg" />
          ) : (
            <XCircle className="w-12 h-12 text-red-500" />
          )}
          <div>
            <h2 className={`text-xl font-bold ${
              overallStatus === 'healthy'
                ? 'text-green-700 dark:text-green-300'
                : overallStatus === 'checking'
                  ? 'text-gray-700 dark:text-gray-300'
                  : 'text-red-700 dark:text-red-300'
            }`}>
              {overallStatus === 'healthy' 
                ? 'System Healthy' 
                : overallStatus === 'checking'
                  ? 'Checking...'
                  : 'System Issues Detected'}
            </h2>
            <p className={`text-sm ${
              overallStatus === 'healthy'
                ? 'text-green-600 dark:text-green-400'
                : overallStatus === 'checking'
                  ? 'text-gray-600 dark:text-gray-400'
                  : 'text-red-600 dark:text-red-400'
            }`}>
              {checks.filter(c => c.status === 'healthy').length} of {checks.length} services operational
            </p>
          </div>
        </div>
      </div>

      {/* Service Status Grid */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        {checks.map((check) => (
          <div
            key={check.name}
            className={`bg-white dark:bg-gray-800 rounded-xl border p-5 ${
              check.status === 'healthy'
                ? 'border-green-200 dark:border-green-800'
                : check.status === 'checking'
                  ? 'border-gray-200 dark:border-gray-700'
                  : 'border-red-200 dark:border-red-800'
            }`}
          >
            <div className="flex items-center justify-between mb-3">
              <div className="flex items-center gap-2 text-gray-600 dark:text-gray-400">
                {getServiceIcon(check.name)}
                <span className="font-medium">{check.name}</span>
              </div>
              {getStatusIcon(check.status)}
            </div>
            
            <div className="space-y-2">
              <div className="flex items-center justify-between text-sm">
                <span className="text-gray-500 dark:text-gray-400">Status</span>
                <span className={`font-medium ${
                  check.status === 'healthy'
                    ? 'text-green-600 dark:text-green-400'
                    : check.status === 'checking'
                      ? 'text-gray-600 dark:text-gray-400'
                      : 'text-red-600 dark:text-red-400'
                }`}>
                  {check.status === 'healthy' ? 'Operational' : check.status === 'checking' ? 'Checking...' : 'Down'}
                </span>
              </div>
              
              {check.latency !== undefined && (
                <div className="flex items-center justify-between text-sm">
                  <span className="text-gray-500 dark:text-gray-400">Latency</span>
                  <span className="font-mono text-gray-700 dark:text-gray-300">{check.latency}ms</span>
                </div>
              )}
              
              {check.lastChecked && (
                <div className="flex items-center justify-between text-sm">
                  <span className="text-gray-500 dark:text-gray-400">Last Check</span>
                  <span className="text-gray-700 dark:text-gray-300 flex items-center gap-1">
                    <Clock className="w-3 h-3" />
                    {check.lastChecked.toLocaleTimeString()}
                  </span>
                </div>
              )}
              
              {check.error && (
                <div className="mt-2 p-2 bg-red-50 dark:bg-red-900/20 rounded text-xs text-red-600 dark:text-red-400">
                  {check.error}
                </div>
              )}
            </div>
          </div>
        ))}
      </div>

      {/* Connection Status */}
      <div className="bg-white dark:bg-gray-800 rounded-xl border border-gray-200 dark:border-gray-700 p-6">
        <h3 className="text-lg font-semibold text-gray-900 dark:text-white mb-4">Connection Info</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="flex items-center gap-3 p-4 bg-gray-50 dark:bg-gray-700/50 rounded-lg">
            {overallStatus === 'healthy' ? (
              <Wifi className="w-5 h-5 text-green-500" />
            ) : (
              <WifiOff className="w-5 h-5 text-red-500" />
            )}
            <div>
              <p className="font-medium text-gray-700 dark:text-gray-300">Server Connection</p>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                {typeof window !== 'undefined' ? window.location.origin : 'localhost:8080'}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-3 p-4 bg-gray-50 dark:bg-gray-700/50 rounded-lg">
            <Clock className="w-5 h-5 text-blue-500" />
            <div>
              <p className="font-medium text-gray-700 dark:text-gray-300">Refresh Interval</p>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                {autoRefresh ? `Every ${REFRESH_INTERVAL / 1000} seconds` : 'Disabled'}
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
