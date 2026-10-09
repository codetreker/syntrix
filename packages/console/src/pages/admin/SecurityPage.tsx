import { useState, useEffect, useCallback } from 'react'
import { adminApi } from '../../lib/admin'
import type { RuleSet } from '../../lib/admin'
import { RulesViewer, RulesEditor } from '../../components/features/rules'
import { Spinner } from '../../components/ui/Loading'
import { useToast } from '../../components/ui/toast-context'
import { Button } from '../../components/ui/Button'
import { Shield, RefreshCw, Edit2, Eye } from 'lucide-react'

export default function SecurityPage() {
  const [rules, setRules] = useState<RuleSet | null>(null)
  const [rawYaml, setRawYaml] = useState<string>('')
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const [isEditing, setIsEditing] = useState(false)
  const toast = useToast()

  const fetchRules = useCallback(async () => {
    setIsLoading(true)
    try {
      const data = await adminApi.getRules()
      setRules(data)
      // Convert to YAML for editor
      setRawYaml(convertRulesToYaml(data))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to load rules')
    } finally {
      setIsLoading(false)
    }
  }, [toast])

  useEffect(() => {
    fetchRules()
  }, [fetchRules])

  const handleSave = async (content: string) => {
    setIsSaving(true)
    try {
      await adminApi.pushRules(content)
      toast.success('Rules updated successfully')
      setIsEditing(false)
      // Refetch to show updated rules
      await fetchRules()
    } finally {
      setIsSaving(false)
    }
  }

  const handleCancelEdit = () => {
    setIsEditing(false)
    // Reset to original YAML
    if (rules) {
      setRawYaml(convertRulesToYaml(rules))
    }
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="p-2 bg-green-100 dark:bg-green-900/30 rounded-lg">
            <Shield className="w-6 h-6 text-green-600 dark:text-green-400" />
          </div>
          <div>
            <h1 className="text-2xl font-bold text-gray-900 dark:text-white">Security Rules</h1>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Manage access control rules for your data
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {!isEditing && (
            <>
              <Button
                variant="secondary"
                onClick={fetchRules}
                disabled={isLoading}
              >
                <RefreshCw className={`w-4 h-4 mr-2 ${isLoading ? 'animate-spin' : ''}`} />
                Refresh
              </Button>
              <Button
                variant="primary"
                onClick={() => setIsEditing(true)}
                disabled={isLoading}
              >
                <Edit2 className="w-4 h-4 mr-2" />
                Edit Rules
              </Button>
            </>
          )}
          {isEditing && (
            <Button
              variant="secondary"
              onClick={() => setIsEditing(false)}
            >
              <Eye className="w-4 h-4 mr-2" />
              View Mode
            </Button>
          )}
        </div>
      </div>

      {/* Content */}
      <div className="bg-white dark:bg-gray-900 rounded-xl border border-gray-200 dark:border-gray-800 overflow-hidden">
        {isLoading ? (
          <div className="p-8 flex justify-center">
            <Spinner size="lg" />
          </div>
        ) : isEditing ? (
          <div className="p-6">
            <RulesEditor
              initialContent={rawYaml}
              onSave={handleSave}
              onCancel={handleCancelEdit}
              isLoading={isSaving}
            />
          </div>
        ) : rules ? (
          <div className="p-6">
            <RulesViewer rules={rules} />
          </div>
        ) : (
          <div className="p-8 text-center text-gray-500 dark:text-gray-400">
            No rules found
          </div>
        )}
      </div>

      {/* Help section */}
      {!isEditing && (
        <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-lg p-4">
          <h3 className="font-medium text-blue-800 dark:text-blue-200 mb-2">
            About Security Rules
          </h3>
          <div className="text-sm text-blue-700 dark:text-blue-300 space-y-1">
            <p>
              Security rules define who can read, write, create, and delete documents in your collections.
            </p>
            <p>
              Rules use a path-based matching system with conditions that can reference the request context
              (e.g., <code className="bg-blue-100 dark:bg-blue-800 px-1 rounded">request.auth.uid</code>).
            </p>
          </div>
        </div>
      )}
    </div>
  )
}

// Helper to convert RuleSet to YAML string
function convertRulesToYaml(rules: RuleSet): string {
  let result = `rules_version: "${rules.rules_version || '1'}"\n`
  result += `service: ${rules.service || 'syntrix'}\n`
  
  if (rules.match && Object.keys(rules.match).length > 0) {
    result += `match:\n`
    result += convertMatchToYaml(rules.match, 1)
  }
  
  return result
}

function convertMatchToYaml(match: Record<string, RuleSet['match'][string]>, indent: number): string {
  let result = ''
  const spaces = '  '.repeat(indent)
  
  for (const [path, block] of Object.entries(match)) {
    result += `${spaces}${path}:\n`
    
    if (block.allow && Object.keys(block.allow).length > 0) {
      result += `${spaces}  allow:\n`
      for (const [method, condition] of Object.entries(block.allow)) {
        result += `${spaces}    ${method}: ${condition || 'true'}\n`
      }
    }
    
    if (block.match && Object.keys(block.match).length > 0) {
      result += `${spaces}  match:\n`
      result += convertMatchToYaml(block.match, indent + 2)
    }
  }
  
  return result
}
