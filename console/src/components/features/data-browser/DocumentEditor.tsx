import { useState, useEffect } from 'react';
import { X, Save } from 'lucide-react';
import type { Document } from '../../../lib/documents';
import { documentsApi } from '../../../lib/documents';
import { Button, Input } from '../../ui';

interface DocumentEditorProps {
  document: Document | null; // null for create mode
  database: string;
  collection: string;
  onClose: () => void;
  onSave: (doc: Document) => void;
}

export function DocumentEditor({ document, database, collection, onClose, onSave }: DocumentEditorProps) {
  const isCreateMode = document === null;
  const [documentId, setDocumentId] = useState('');
  const [jsonContent, setJsonContent] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (document) {
      // Edit mode - prepare JSON without metadata fields
      const { id, _collection, _createdAt, _updatedAt, ...rest } = document;
      setDocumentId(id);
      setJsonContent(JSON.stringify(rest, null, 2));
    } else {
      // Create mode
      setDocumentId('');
      setJsonContent('{\n  \n}');
    }
  }, [document]);

  const handleSave = async () => {
    setError(null);
    
    // Validate JSON
    let parsedData: Record<string, unknown>;
    try {
      parsedData = JSON.parse(jsonContent);
    } catch {
      setError('Invalid JSON format');
      return;
    }

    // Validate ID for create mode
    if (isCreateMode && !documentId.trim()) {
      setError('Document ID is required');
      return;
    }

    setSaving(true);
    try {
      let savedDoc: Document;
      if (isCreateMode) {
        savedDoc = await documentsApi.create(database, collection, parsedData, documentId);
      } else {
        savedDoc = await documentsApi.update(database, collection, document!.id, parsedData);
      }
      onSave(savedDoc);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save document');
    } finally {
      setSaving(false);
    }
  };

  const handleFormat = () => {
    try {
      const parsed = JSON.parse(jsonContent);
      setJsonContent(JSON.stringify(parsed, null, 2));
      setError(null);
    } catch {
      setError('Cannot format: Invalid JSON');
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-full max-w-2xl mx-4 bg-white dark:bg-gray-800 rounded-lg shadow-xl flex flex-col max-h-[80vh]">
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-200 dark:border-gray-700">
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            {isCreateMode ? 'Create Document' : 'Edit Document'}
          </h2>
          <button
            onClick={onClose}
            className="p-2 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-auto p-6 space-y-4">
          {/* Collection & ID */}
          <div className="flex gap-4">
            <div className="flex-1">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                Collection
              </label>
              <input
                type="text"
                value={collection}
                disabled
                className="w-full px-3 py-2 bg-gray-100 dark:bg-gray-700 rounded-md text-gray-500 dark:text-gray-400 text-sm"
              />
            </div>
            <div className="flex-1">
              <Input
                label="Document ID"
                value={documentId}
                onChange={(e) => setDocumentId(e.target.value)}
                disabled={!isCreateMode}
                placeholder={isCreateMode ? 'Enter document ID' : undefined}
              />
            </div>
          </div>

          {/* JSON Editor */}
          <div>
            <div className="flex items-center justify-between mb-1">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300">
                Document Data (JSON)
              </label>
              <button
                onClick={handleFormat}
                className="text-xs text-blue-600 hover:text-blue-700 dark:text-blue-400"
              >
                Format JSON
              </button>
            </div>
            <textarea
              value={jsonContent}
              onChange={(e) => setJsonContent(e.target.value)}
              rows={15}
              className="w-full px-3 py-2 font-mono text-sm rounded-md border border-gray-300 dark:border-gray-600 bg-gray-50 dark:bg-gray-900 text-gray-900 dark:text-gray-100 focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
              placeholder="Enter JSON document data"
            />
          </div>

          {/* Error */}
          {error && (
            <div className="p-3 bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800 rounded-md">
              <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-end gap-3 px-6 py-4 border-t border-gray-200 dark:border-gray-700">
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button variant="primary" onClick={handleSave} loading={saving}>
            <Save className="w-4 h-4 mr-2" />
            {isCreateMode ? 'Create' : 'Save Changes'}
          </Button>
        </div>
      </div>
    </div>
  );
}
