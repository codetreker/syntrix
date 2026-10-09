import { useState } from 'react'
import { adminApi } from '../../../lib/admin'
import type { User, UpdateUserRequest } from '../../../lib/admin'
import { Button } from '../../ui/Button'
import { Modal } from '../../ui/Modal'
import { X, Check, Shield, ShieldOff, Edit2 } from 'lucide-react'

interface UserListProps {
  users: User[]
  onUserUpdated: () => void
  onError: (message: string) => void
  onSuccess: (message: string) => void
}

export function UserList({ users, onUserUpdated, onError, onSuccess }: UserListProps) {
  const [editingUser, setEditingUser] = useState<User | null>(null)
  const [editRoles, setEditRoles] = useState<string[]>([])
  const [editDisabled, setEditDisabled] = useState(false)
  const [isUpdating, setIsUpdating] = useState(false)
  const [showDisableConfirm, setShowDisableConfirm] = useState(false)

  const handleEditClick = (user: User) => {
    setEditingUser(user)
    setEditRoles([...user.roles])
    setEditDisabled(user.disabled)
  }

  const handleSave = async () => {
    if (!editingUser) return

    // If disabling a currently active user, ask for confirmation first
    if (editDisabled && !editingUser.disabled && !showDisableConfirm) {
      setShowDisableConfirm(true)
      return
    }
    setShowDisableConfirm(false)

    setIsUpdating(true)
    try {
      const data: UpdateUserRequest = {
        roles: editRoles,
        disabled: editDisabled
      }
      await adminApi.updateUser(editingUser.id, data)
      onSuccess(`User "${editingUser.username}" updated successfully`)
      setEditingUser(null)
      onUserUpdated()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to update user')
    } finally {
      setIsUpdating(false)
    }
  }

  const toggleRole = (role: string) => {
    if (editRoles.includes(role)) {
      setEditRoles(editRoles.filter(r => r !== role))
    } else {
      setEditRoles([...editRoles, role])
    }
  }

  const formatDate = (dateStr: string) => {
    if (!dateStr || dateStr === '0001-01-01T00:00:00Z') return '-'
    try {
      return new Date(dateStr).toLocaleString()
    } catch {
      return dateStr
    }
  }

  return (
    <>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-200 dark:border-gray-700">
              <th className="text-left p-3 font-medium text-gray-600 dark:text-gray-400">Username</th>
              <th className="text-left p-3 font-medium text-gray-600 dark:text-gray-400">Roles</th>
              <th className="text-left p-3 font-medium text-gray-600 dark:text-gray-400">Status</th>
              <th className="text-left p-3 font-medium text-gray-600 dark:text-gray-400">Created</th>
              <th className="text-left p-3 font-medium text-gray-600 dark:text-gray-400">Last Login</th>
              <th className="text-right p-3 font-medium text-gray-600 dark:text-gray-400">Actions</th>
            </tr>
          </thead>
          <tbody>
            {users.map((user) => (
              <tr 
                key={user.id} 
                className="border-b border-gray-100 dark:border-gray-800 hover:bg-gray-50 dark:hover:bg-gray-800/50"
              >
                <td className="p-3">
                  <div className="flex items-center gap-2">
                    <div className="w-8 h-8 rounded-full bg-blue-100 dark:bg-blue-900 flex items-center justify-center text-blue-600 dark:text-blue-400 font-medium">
                      {user.username.charAt(0).toUpperCase()}
                    </div>
                    <span className="font-medium text-gray-900 dark:text-gray-100">{user.username}</span>
                  </div>
                </td>
                <td className="p-3">
                  <div className="flex flex-wrap gap-1">
                    {user.roles.length > 0 ? (
                      user.roles.map((role) => (
                        <span 
                          key={role}
                          className={`px-2 py-0.5 rounded text-xs font-medium ${
                            role === 'admin' 
                              ? 'bg-purple-100 text-purple-700 dark:bg-purple-900 dark:text-purple-300' 
                              : 'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300'
                          }`}
                        >
                          {role}
                        </span>
                      ))
                    ) : (
                      <span className="text-gray-400">No roles</span>
                    )}
                  </div>
                </td>
                <td className="p-3">
                  {user.disabled ? (
                    <span className="inline-flex items-center gap-1 text-red-600 dark:text-red-400">
                      <ShieldOff className="w-4 h-4" />
                      Disabled
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 text-green-600 dark:text-green-400">
                      <Shield className="w-4 h-4" />
                      Active
                    </span>
                  )}
                </td>
                <td className="p-3 text-gray-600 dark:text-gray-400">
                  {formatDate(user.createdAt)}
                </td>
                <td className="p-3 text-gray-600 dark:text-gray-400">
                  {formatDate(user.last_login_at || '')}
                </td>
                <td className="p-3 text-right">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleEditClick(user)}
                  >
                    <Edit2 className="w-4 h-4" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Edit User Modal */}
      <Modal
        isOpen={!!editingUser}
        onClose={() => setEditingUser(null)}
        title={`Edit User: ${editingUser?.username}`}
      >
        {editingUser && (
          <div className="space-y-4">
            {/* Status Toggle */}
            <div>
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
                Account Status
              </label>
              <div className="flex items-center gap-4">
                <button
                  type="button"
                  onClick={() => setEditDisabled(false)}
                  className={`flex items-center gap-2 px-4 py-2 rounded-lg border transition-colors ${
                    !editDisabled 
                      ? 'border-green-500 bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-400' 
                      : 'border-gray-200 dark:border-gray-700 text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-800'
                  }`}
                >
                  <Check className="w-4 h-4" />
                  Active
                </button>
                <button
                  type="button"
                  onClick={() => setEditDisabled(true)}
                  className={`flex items-center gap-2 px-4 py-2 rounded-lg border transition-colors ${
                    editDisabled 
                      ? 'border-red-500 bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-400' 
                      : 'border-gray-200 dark:border-gray-700 text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-800'
                  }`}
                >
                  <X className="w-4 h-4" />
                  Disabled
                </button>
              </div>
            </div>

            {/* Role Editor */}
            <div>
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
                Roles
              </label>
              <div className="flex flex-wrap gap-2">
                {['admin', 'user', 'viewer'].map((role) => (
                  <button
                    key={role}
                    type="button"
                    onClick={() => toggleRole(role)}
                    className={`px-3 py-1.5 rounded-lg border transition-colors ${
                      editRoles.includes(role)
                        ? role === 'admin'
                          ? 'border-purple-500 bg-purple-50 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
                          : 'border-blue-500 bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400'
                        : 'border-gray-200 dark:border-gray-700 text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-800'
                    }`}
                  >
                    {role}
                  </button>
                ))}
              </div>
              <p className="mt-2 text-xs text-gray-500 dark:text-gray-400">
                Click to toggle roles. Admin role grants full access.
              </p>
            </div>

            {/* Actions */}
            <div className="flex justify-end gap-2 pt-4 border-t border-gray-200 dark:border-gray-700">
              <Button
                variant="secondary"
                onClick={() => setEditingUser(null)}
                disabled={isUpdating}
              >
                Cancel
              </Button>
              <Button
                variant="primary"
                onClick={handleSave}
                disabled={isUpdating}
              >
                {isUpdating ? 'Saving...' : 'Save Changes'}
              </Button>
            </div>
          </div>
        )}
      </Modal>

      {/* Disable Confirmation Modal */}
      <Modal
        isOpen={showDisableConfirm}
        onClose={() => setShowDisableConfirm(false)}
        title="Confirm Disable User"
      >
        <div className="space-y-4">
          <p className="text-gray-700 dark:text-gray-300">
            Are you sure you want to disable <strong>{editingUser?.username}</strong>? They will not be able to log in.
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowDisableConfirm(false)}>Cancel</Button>
            <Button variant="danger" onClick={handleSave}>Disable User</Button>
          </div>
        </div>
      </Modal>
    </>
  )
}
