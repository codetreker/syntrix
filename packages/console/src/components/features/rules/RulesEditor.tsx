import { useState } from 'react'
import { Button } from '../../ui/Button'
import { Modal } from '../../ui/Modal'
import { AlertTriangle, Save, X } from 'lucide-react'

interface RulesEditorProps {
  initialContent: string
  onSave: (content: string) => Promise<void>
  onCancel: () => void
  isLoading?: boolean
}

export function RulesEditor({ initialContent, onSave, onCancel, isLoading }: RulesEditorProps) {
  const [content, setContent] = useState(initialContent)
  const [showConfirm, setShowConfirm] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const hasChanges = content !== initialContent

  const handleSave = async () => {
    setError(null)
    try {
      await onSave(content)
      setShowConfirm(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save rules')
    }
  }

  const handleCancel = () => {
    if (hasChanges) {
      if (confirm('You have unsaved changes. Are you sure you want to cancel?')) {
        onCancel()
      }
    } else {
      onCancel()
    }
  }

  return (
    <div className="space-y-4">
      {/* Warning banner */}
      <div className="flex items-start gap-3 p-4 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg">
        <AlertTriangle className="w-5 h-5 text-amber-600 dark:text-amber-400 shrink-0 mt-0.5" />
        <div className="text-sm">
          <p className="font-medium text-amber-800 dark:text-amber-200">
            Caution: Editing Security Rules
          </p>
          <p className="text-amber-700 dark:text-amber-300 mt-1">
            Changes to security rules take effect immediately. Make sure your YAML syntax is correct
            before pushing. Invalid rules may lock you out of certain resources.
          </p>
        </div>
      </div>

      {/* Editor */}
      <div className="relative">
        <textarea
          value={content}
          onChange={(e) => setContent(e.target.value)}
          className="w-full h-96 font-mono text-sm p-4 bg-gray-900 text-gray-100 rounded-lg border border-gray-700 focus:border-blue-500 focus:ring-1 focus:ring-blue-500 resize-none"
          placeholder="Enter YAML rules..."
          spellCheck={false}
        />
        <div className="absolute top-2 right-2 flex items-center gap-2">
          {hasChanges && (
            <span className="text-xs bg-amber-500 text-white px-2 py-0.5 rounded">
              Unsaved
            </span>
          )}
        </div>
      </div>

      {/* Error display */}
      {error && (
        <div className="p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg text-red-700 dark:text-red-300 text-sm">
          {error}
        </div>
      )}

      {/* Actions */}
      <div className="flex justify-end gap-3">
        <Button
          variant="secondary"
          onClick={handleCancel}
          disabled={isLoading}
        >
          <X className="w-4 h-4 mr-1" />
          Cancel
        </Button>
        <Button
          variant="primary"
          onClick={() => setShowConfirm(true)}
          disabled={isLoading || !hasChanges}
        >
          <Save className="w-4 h-4 mr-1" />
          Push Rules
        </Button>
      </div>

      {/* Confirmation Modal */}
      <Modal
        isOpen={showConfirm}
        onClose={() => setShowConfirm(false)}
        title="Confirm Rule Push"
      >
        <div className="space-y-4">
          <p className="text-gray-600 dark:text-gray-400">
            Are you sure you want to push these rules? This will immediately update the security
            configuration for all resources.
          </p>
          <div className="flex justify-end gap-3">
            <Button
              variant="secondary"
              onClick={() => setShowConfirm(false)}
              disabled={isLoading}
            >
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={handleSave}
              disabled={isLoading}
            >
              {isLoading ? 'Pushing...' : 'Yes, Push Rules'}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  )
}
