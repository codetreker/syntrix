import { useState, useEffect, useCallback, useRef } from 'react'
import { useAuthStore } from '../stores'
import { adminApi, type User, type DatabaseInfo, type RuleSet } from '../lib/admin'
import { api } from '../lib/api'
import {
  Database,
  Users,
  Shield,
  RefreshCw,
  CheckCircle,
  XCircle,
  AlertTriangle,
  Clock,
  Heart,
} from 'lucide-react'
import { Button } from '../components/ui/Button'

const AUTO_REFRESH_MS = 30_000

// ─── Types ───────────────────────────────────────────────────────────

interface DashboardData {
  users: User[]
  databases: DatabaseInfo[]
  rules: RuleSet | null
  healthOk: boolean
  adminHealthOk: boolean
}

type LoadState = 'loading' | 'error' | 'ready'

// ─── Skeleton ────────────────────────────────────────────────────────

function Skeleton({ className = '' }: { className?: string }) {
  return (
    <div
      className={`animate-pulse rounded bg-gray-200 dark:bg-gray-700 ${className}`}
    />
  )
}

// ─── Stat Card ───────────────────────────────────────────────────────

function StatCard({
  title,
  value,
  icon,
  loading,
  color = 'blue',
}: {
  title: string
  value: number | string
  icon: React.ReactNode
  loading?: boolean
  color?: string
}) {
  const bg: Record<string, string> = {
    blue: 'bg-blue-100 text-blue-600 dark:bg-blue-900/30 dark:text-blue-400',
    green: 'bg-green-100 text-green-600 dark:bg-green-900/30 dark:text-green-400',
    purple: 'bg-purple-100 text-purple-600 dark:bg-purple-900/30 dark:text-purple-400',
    amber: 'bg-amber-100 text-amber-600 dark:bg-amber-900/30 dark:text-amber-400',
  }

  return (
    <div className="bg-white dark:bg-gray-800 rounded-xl shadow-sm border border-gray-200 dark:border-gray-700 p-4 sm:p-6">
      <div className="flex items-center justify-between">
        <div className="min-w-0">
          <p className="text-sm font-medium text-gray-500 dark:text-gray-400 truncate">
            {title}
          </p>
          {loading ? (
            <Skeleton className="mt-2 h-9 w-16" />
          ) : (
            <p className="mt-2 text-3xl font-bold text-gray-900 dark:text-white truncate">
              {value}
            </p>
          )}
        </div>
        <div className={`p-3 rounded-lg shrink-0 ${bg[color]}`}>{icon}</div>
      </div>
    </div>
  )
}

// ─── Health Card ─────────────────────────────────────────────────────

function HealthCard({
  healthOk,
  adminHealthOk,
  lastChecked,
  loading,
}: {
  healthOk: boolean
  adminHealthOk: boolean
  lastChecked: Date | null
  loading?: boolean
}) {
  const allGood = healthOk && adminHealthOk

  return (
    <div className="bg-white dark:bg-gray-800 rounded-xl shadow-sm border border-gray-200 dark:border-gray-700 p-4 sm:p-6">
      <div className="flex items-center justify-between">
        <div>
          <p className="text-sm font-medium text-gray-500 dark:text-gray-400">
            System Health
          </p>
          {loading ? (
            <Skeleton className="mt-2 h-9 w-24" />
          ) : (
            <p
              className={`mt-2 text-2xl font-bold ${
                allGood
                  ? 'text-green-600 dark:text-green-400'
                  : 'text-red-600 dark:text-red-400'
              }`}
            >
              {allGood ? 'Healthy' : 'Degraded'}
            </p>
          )}
          {lastChecked && (
            <p className="mt-1 text-xs text-gray-400 dark:text-gray-500 flex items-center gap-1">
              <Clock className="w-3 h-3" />
              {lastChecked.toLocaleTimeString()}
            </p>
          )}
        </div>
        <div
          className={`p-3 rounded-lg ${
            loading
              ? 'bg-gray-100 text-gray-400 dark:bg-gray-800 dark:text-gray-500'
              : allGood
                ? 'bg-green-100 text-green-600 dark:bg-green-900/30 dark:text-green-400'
                : 'bg-red-100 text-red-600 dark:bg-red-900/30 dark:text-red-400'
          }`}
        >
          {allGood ? (
            <CheckCircle className="w-6 h-6" />
          ) : (
            <XCircle className="w-6 h-6" />
          )}
        </div>
      </div>
      {!loading && (
        <div className="mt-4 space-y-2 text-sm">
          <StatusRow label="Core API" ok={healthOk} />
          <StatusRow label="Admin API" ok={adminHealthOk} />
        </div>
      )}
    </div>
  )
}

function StatusRow({ label, ok }: { label: string; ok: boolean }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-gray-600 dark:text-gray-400">{label}</span>
      <span
        className={`flex items-center gap-1 font-medium ${
          ok
            ? 'text-green-600 dark:text-green-400'
            : 'text-red-600 dark:text-red-400'
        }`}
      >
        {ok ? (
          <CheckCircle className="w-3.5 h-3.5" />
        ) : (
          <XCircle className="w-3.5 h-3.5" />
        )}
        {ok ? 'OK' : 'Down'}
      </span>
    </div>
  )
}

// ─── Databases Overview ──────────────────────────────────────────────

function DatabasesTable({
  databases,
  loading,
}: {
  databases: DatabaseInfo[]
  loading?: boolean
}) {
  return (
    <div className="bg-white dark:bg-gray-800 rounded-xl shadow-sm border border-gray-200 dark:border-gray-700 p-4 sm:p-6">
      <h3 className="text-lg font-semibold text-gray-900 dark:text-white mb-4">
        Databases
      </h3>
      {loading ? (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </div>
      ) : databases.length === 0 ? (
        <p className="text-sm text-gray-500 dark:text-gray-400">
          No databases found.
        </p>
      ) : (
        <div className="overflow-x-auto -mx-4 sm:mx-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-200 dark:border-gray-700">
                <th className="text-left py-2 px-4 sm:px-2 font-medium text-gray-500 dark:text-gray-400">
                  Name
                </th>
              </tr>
            </thead>
            <tbody>
              {databases.map((db) => (
                <tr
                  key={db.name}
                  className="border-b border-gray-100 dark:border-gray-700/50"
                >
                  <td className="py-2 px-4 sm:px-2 text-gray-900 dark:text-white flex items-center gap-2">
                    <Database className="w-4 h-4 text-blue-500 shrink-0" />
                    <span className="truncate">{db.name}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ─── Users Table ─────────────────────────────────────────────────────

function UsersTable({
  users,
  loading,
}: {
  users: User[]
  loading?: boolean
}) {
  return (
    <div className="bg-white dark:bg-gray-800 rounded-xl shadow-sm border border-gray-200 dark:border-gray-700 p-4 sm:p-6">
      <h3 className="text-lg font-semibold text-gray-900 dark:text-white mb-4">
        Users
      </h3>
      {loading ? (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </div>
      ) : users.length === 0 ? (
        <p className="text-sm text-gray-500 dark:text-gray-400">
          No users found.
        </p>
      ) : (
        <div className="overflow-x-auto -mx-4 sm:mx-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-200 dark:border-gray-700">
                <th className="text-left py-2 px-4 sm:px-2 font-medium text-gray-500 dark:text-gray-400">
                  Username
                </th>
                <th className="text-left py-2 px-4 sm:px-2 font-medium text-gray-500 dark:text-gray-400 hidden sm:table-cell">
                  Database
                </th>
                <th className="text-left py-2 px-4 sm:px-2 font-medium text-gray-500 dark:text-gray-400 hidden md:table-cell">
                  Roles
                </th>
                <th className="text-left py-2 px-4 sm:px-2 font-medium text-gray-500 dark:text-gray-400">
                  Status
                </th>
              </tr>
            </thead>
            <tbody>
              {users.slice(0, 10).map((u) => (
                <tr
                  key={u.id}
                  className="border-b border-gray-100 dark:border-gray-700/50"
                >
                  <td className="py-2 px-4 sm:px-2 text-gray-900 dark:text-white truncate max-w-[120px]">
                    {u.username}
                  </td>
                  <td className="py-2 px-4 sm:px-2 text-gray-600 dark:text-gray-400 hidden sm:table-cell">
                    {u.database}
                  </td>
                  <td className="py-2 px-4 sm:px-2 text-gray-600 dark:text-gray-400 hidden md:table-cell">
                    {u.roles?.join(', ') || '-'}
                  </td>
                  <td className="py-2 px-4 sm:px-2">
                    <span
                      className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                        u.disabled
                          ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
                          : 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                      }`}
                    >
                      {u.disabled ? 'Disabled' : 'Active'}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {users.length > 10 && (
            <p className="mt-2 text-xs text-gray-400 dark:text-gray-500">
              Showing 10 of {users.length} users
            </p>
          )}
        </div>
      )}
    </div>
  )
}

// ─── Error State ─────────────────────────────────────────────────────

function ErrorBanner({
  message,
  onRetry,
}: {
  message: string
  onRetry: () => void
}) {
  return (
    <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl p-4 sm:p-6 flex flex-col sm:flex-row items-start sm:items-center gap-3">
      <AlertTriangle className="w-5 h-5 text-red-500 shrink-0" />
      <p className="text-sm text-red-700 dark:text-red-400 flex-1">
        {message}
      </p>
      <Button variant="secondary" onClick={onRetry} className="shrink-0">
        <RefreshCw className="w-4 h-4 mr-2" />
        Retry
      </Button>
    </div>
  )
}

// ─── Page ────────────────────────────────────────────────────────────

export default function DashboardPage() {
  const { user } = useAuthStore()

  const [data, setData] = useState<DashboardData>({
    users: [],
    databases: [],
    rules: null,
    healthOk: false,
    adminHealthOk: false,
  })
  const [state, setState] = useState<LoadState>('loading')
  const [lastChecked, setLastChecked] = useState<Date | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const fetchAll = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true)
    else setState('loading')

    try {
      const [users, databases, rules, healthRes, adminHealthOk] =
        await Promise.all([
          adminApi.listUsers(100, 0).catch(() => [] as User[]),
          adminApi.listDatabases().catch(() => [] as DatabaseInfo[]),
          adminApi.getRules().catch(() => null),
          api
            .get('/health')
            .then(() => true)
            .catch(() => false),
          adminApi.health(),
        ])

      setData({ users, databases, rules, healthOk: healthRes, adminHealthOk })
      setLastChecked(new Date())
      setState('ready')
    } catch {
      setState('error')
    } finally {
      setRefreshing(false)
    }
  }, [])

  // Initial fetch + auto-refresh every 30s
  useEffect(() => {
    fetchAll()
    timerRef.current = setInterval(() => fetchAll(true), AUTO_REFRESH_MS)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
    }
  }, [fetchAll])

  const rulesCount = data.rules
    ? Object.keys(data.rules.match || {}).length
    : 0

  const isLoading = state === 'loading'

  return (
    <div className="space-y-6 max-w-full overflow-hidden">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
            Dashboard
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400 mt-1">
            Welcome back, {user?.username || 'User'}
          </p>
        </div>
        <Button
          variant="secondary"
          onClick={() => fetchAll(true)}
          disabled={refreshing}
        >
          <RefreshCw
            className={`w-4 h-4 mr-2 ${refreshing ? 'animate-spin' : ''}`}
          />
          Refresh
        </Button>
      </div>

      {/* Error state */}
      {state === 'error' && (
        <ErrorBanner
          message="Failed to load dashboard data. The server may be unreachable."
          onRetry={() => fetchAll()}
        />
      )}

      {/* Stat cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 sm:gap-6">
        <StatCard
          title="Users"
          value={data.users.length}
          icon={<Users className="w-6 h-6" />}
          loading={isLoading}
          color="purple"
        />
        <StatCard
          title="Databases"
          value={data.databases.length}
          icon={<Database className="w-6 h-6" />}
          loading={isLoading}
          color="blue"
        />
        <StatCard
          title="Security Rules"
          value={rulesCount}
          icon={<Shield className="w-6 h-6" />}
          loading={isLoading}
          color="amber"
        />
        <StatCard
          title="Health"
          value={
            isLoading
              ? '-'
              : data.healthOk && data.adminHealthOk
                ? 'OK'
                : 'Degraded'
          }
          icon={<Heart className="w-6 h-6" />}
          loading={isLoading}
          color="green"
        />
      </div>

      {/* Health detail + databases */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 sm:gap-6">
        <HealthCard
          healthOk={data.healthOk}
          adminHealthOk={data.adminHealthOk}
          lastChecked={lastChecked}
          loading={isLoading}
        />
        <DatabasesTable databases={data.databases} loading={isLoading} />
      </div>

      {/* Users table */}
      <UsersTable users={data.users} loading={isLoading} />
    </div>
  )
}
