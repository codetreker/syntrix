import { useState } from 'react'
import type { RuleSet } from '../../../lib/admin'
import { Button } from '../../ui/Button'
import { Copy, Check, ChevronRight, ChevronDown } from 'lucide-react'

interface RulesViewerProps {
  rules: RuleSet
}

interface MatchBlockType {
  allow?: Record<string, string>
  match?: Record<string, MatchBlockType>
}

// Recursive component for MatchBlock rendering
function MatchBlockView({ 
  path, 
  block, 
  depth = 0 
}: { 
  path: string
  block: MatchBlockType
  depth?: number 
}) {
  const [isExpanded, setIsExpanded] = useState(depth < 2)
  const hasChildren = block.match && Object.keys(block.match).length > 0
  const hasAllow = block.allow && Object.keys(block.allow).length > 0

  return (
    <div className={`${depth > 0 ? 'ml-4 border-l-2 border-gray-200 dark:border-gray-700 pl-4' : ''}`}>
      <div 
        className="flex items-center gap-2 py-1 cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-800 rounded px-2 -mx-2"
        onClick={() => setIsExpanded(!isExpanded)}
      >
        {hasChildren || hasAllow ? (
          isExpanded ? (
            <ChevronDown className="w-4 h-4 text-gray-400" />
          ) : (
            <ChevronRight className="w-4 h-4 text-gray-400" />
          )
        ) : (
          <span className="w-4" />
        )}
        <span className="font-mono text-blue-600 dark:text-blue-400">{path}</span>
      </div>

      {isExpanded && (
        <div className="ml-6 space-y-1">
          {/* Allow rules */}
          {hasAllow && (
            <div className="py-1">
              <span className="text-purple-600 dark:text-purple-400 font-medium">allow:</span>
              <div className="ml-4 space-y-0.5">
                {Object.entries(block.allow!).map(([method, condition]) => (
                  <div key={method} className="flex items-center gap-2 font-mono text-sm">
                    <span className="text-green-600 dark:text-green-400">{method}:</span>
                    <span className="text-gray-600 dark:text-gray-400">{condition || 'true'}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Nested match blocks */}
          {hasChildren && (
            <div className="space-y-1">
              {Object.entries(block.match!).map(([childPath, childBlock]) => (
                <MatchBlockView 
                  key={childPath} 
                  path={childPath} 
                  block={childBlock} 
                  depth={depth + 1} 
                />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function RulesViewer({ rules }: RulesViewerProps) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    try {
      // Convert to YAML-like format for copying
      const yamlContent = convertToYaml(rules)
      await navigator.clipboard.writeText(yamlContent)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (err) {
      console.error('Failed to copy:', err)
    }
  }

  return (
    <div className="space-y-4">
      {/* Header info */}
      <div className="flex items-center justify-between pb-4 border-b border-gray-200 dark:border-gray-700">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <span className="text-sm text-gray-500 dark:text-gray-400">Version:</span>
            <span className="font-mono text-sm bg-gray-100 dark:bg-gray-800 px-2 py-0.5 rounded">
              {rules.rules_version || 'N/A'}
            </span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-sm text-gray-500 dark:text-gray-400">Service:</span>
            <span className="font-mono text-sm bg-gray-100 dark:bg-gray-800 px-2 py-0.5 rounded">
              {rules.service || 'N/A'}
            </span>
          </div>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={handleCopy}
        >
          {copied ? (
            <>
              <Check className="w-4 h-4 mr-1" />
              Copied
            </>
          ) : (
            <>
              <Copy className="w-4 h-4 mr-1" />
              Copy
            </>
          )}
        </Button>
      </div>

      {/* Rules tree */}
      <div className="bg-gray-50 dark:bg-gray-800/50 rounded-lg p-4">
        <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">Match Rules</h3>
        {rules.match && Object.keys(rules.match).length > 0 ? (
          <div className="space-y-2">
            {Object.entries(rules.match).map(([path, block]) => (
              <MatchBlockView key={path} path={path} block={block} />
            ))}
          </div>
        ) : (
          <p className="text-gray-500 dark:text-gray-400 text-sm">No match rules defined</p>
        )}
      </div>
    </div>
  )
}

// Helper to convert RuleSet to YAML-like string
function convertToYaml(rules: RuleSet): string {
  let result = `rules_version: "${rules.rules_version || '1'}"\n`
  result += `service: ${rules.service || 'syntrix'}\n`
  result += `match:\n`

  if (rules.match) {
    for (const [path, block] of Object.entries(rules.match)) {
      result += `  ${path}:\n`
      if (block.allow) {
        result += `    allow:\n`
        for (const [method, condition] of Object.entries(block.allow)) {
          result += `      ${method}: ${condition || 'true'}\n`
        }
      }
      if (block.match) {
        result += `    match:\n`
        result += convertMatchToYaml(block.match, 3)
      }
    }
  }

  return result
}

function convertMatchToYaml(match: Record<string, MatchBlockType>, indent: number): string {
  let result = ''
  const spaces = '  '.repeat(indent)
  for (const [path, block] of Object.entries(match)) {
    result += `${spaces}${path}:\n`
    if (block.allow) {
      result += `${spaces}  allow:\n`
      for (const [method, condition] of Object.entries(block.allow)) {
        result += `${spaces}    ${method}: ${condition || 'true'}\n`
      }
    }
    if (block.match) {
      result += `${spaces}  match:\n`
      result += convertMatchToYaml(block.match, indent + 2)
    }
  }
  return result
}
