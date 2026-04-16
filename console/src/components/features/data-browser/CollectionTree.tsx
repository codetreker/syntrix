import { useState, useEffect } from 'react';
import { Database, ChevronRight, FolderOpen, RefreshCw, ChevronDown } from 'lucide-react';
import { documentsApi, type CollectionInfo } from '../../../lib/documents';
import { adminApi, type DatabaseInfo } from '../../../lib/admin';
import { Spinner } from '../../ui';

interface CollectionTreeProps {
  selectedDatabase: string | null;
  selectedCollection: string | null;
  onSelectDatabase: (db: string) => void;
  onSelectCollection: (name: string) => void;
}

export function CollectionTree({
  selectedDatabase,
  selectedCollection,
  onSelectDatabase,
  onSelectCollection,
}: CollectionTreeProps) {
  const [databases, setDatabases] = useState<DatabaseInfo[]>([]);
  const [collections, setCollections] = useState<CollectionInfo[]>([]);
  const [loadingDbs, setLoadingDbs] = useState(true);
  const [loadingCols, setLoadingCols] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dbDropdownOpen, setDbDropdownOpen] = useState(false);

  const fetchDatabases = async () => {
    setLoadingDbs(true);
    setError(null);
    try {
      const data = await adminApi.listDatabases();
      setDatabases(data);
      // Auto-select first database if none selected
      if (!selectedDatabase && data.length > 0) {
        onSelectDatabase(data[0].name);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load databases');
    } finally {
      setLoadingDbs(false);
    }
  };

  const fetchCollections = async () => {
    if (!selectedDatabase) return;
    setLoadingCols(true);
    setError(null);
    try {
      const data = await documentsApi.listCollections(selectedDatabase);
      setCollections(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load collections');
    } finally {
      setLoadingCols(false);
    }
  };

  useEffect(() => {
    fetchDatabases();
  }, []);

  useEffect(() => {
    if (selectedDatabase) {
      fetchCollections();
    } else {
      setCollections([]);
    }
  }, [selectedDatabase]);

  const handleRefresh = () => {
    if (selectedDatabase) {
      fetchCollections();
    }
  };

  return (
    <div className="w-64 border-r border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800/50 flex flex-col">
      {/* Database Selector */}
      <div className="px-3 py-2 border-b border-gray-200 dark:border-gray-700">
        <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">Database</label>
        {loadingDbs ? (
          <div className="flex items-center justify-center py-2"><Spinner size="sm" /></div>
        ) : (
          <div className="relative">
            <button
              onClick={() => setDbDropdownOpen(!dbDropdownOpen)}
              className="w-full flex items-center justify-between px-3 py-1.5 text-sm rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 hover:border-blue-400 focus:ring-2 focus:ring-blue-500"
            >
              <span className="truncate">{selectedDatabase || 'Select database'}</span>
              <ChevronDown className="w-4 h-4 ml-1 flex-shrink-0" />
            </button>
            {dbDropdownOpen && (
              <div className="absolute z-10 mt-1 w-full bg-white dark:bg-gray-700 border border-gray-200 dark:border-gray-600 rounded-md shadow-lg max-h-48 overflow-auto">
                {databases.map((db) => (
                  <button
                    key={db.name}
                    onClick={() => {
                      onSelectDatabase(db.name);
                      setDbDropdownOpen(false);
                    }}
                    className={`w-full text-left px-3 py-2 text-sm hover:bg-blue-50 dark:hover:bg-blue-900/30 ${
                      selectedDatabase === db.name
                        ? 'bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300'
                        : 'text-gray-700 dark:text-gray-300'
                    }`}
                  >
                    {db.name}
                  </button>
                ))}
                {databases.length === 0 && (
                  <div className="px-3 py-2 text-sm text-gray-500">No databases found</div>
                )}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
        <div className="flex items-center gap-2">
          <Database className="w-4 h-4 text-gray-500" />
          <span className="text-sm font-medium text-gray-700 dark:text-gray-300">Collections</span>
        </div>
        <button
          onClick={handleRefresh}
          disabled={loadingCols || !selectedDatabase}
          className="p-1 rounded hover:bg-gray-200 dark:hover:bg-gray-700 text-gray-500 disabled:opacity-50"
          title="Refresh collections"
        >
          <RefreshCw className={`w-4 h-4 ${loadingCols ? 'animate-spin' : ''}`} />
        </button>
      </div>

      {/* Collection List */}
      <div className="flex-1 overflow-y-auto py-2">
        {!selectedDatabase ? (
          <div className="px-4 py-2 text-sm text-gray-500">Select a database first</div>
        ) : loadingCols && collections.length === 0 ? (
          <div className="flex items-center justify-center py-8">
            <Spinner size="sm" />
          </div>
        ) : error ? (
          <div className="px-4 py-2 text-sm text-red-600 dark:text-red-400">{error}</div>
        ) : collections.length === 0 ? (
          <div className="px-4 py-2 text-sm text-gray-500">No collections found</div>
        ) : (
          <ul className="space-y-0.5">
            {collections.map((collection) => {
              const isSelected = selectedCollection === collection.name;
              return (
                <li key={collection.name}>
                  <button
                    onClick={() => onSelectCollection(collection.name)}
                    className={`
                      w-full flex items-center gap-2 px-4 py-2 text-sm text-left transition-colors
                      ${isSelected
                        ? 'bg-blue-100 dark:bg-blue-900/30 text-blue-700 dark:text-blue-300'
                        : 'text-gray-700 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700/50'
                      }
                    `}
                  >
                    {isSelected ? (
                      <FolderOpen className="w-4 h-4 flex-shrink-0" />
                    ) : (
                      <ChevronRight className="w-4 h-4 flex-shrink-0" />
                    )}
                    <span className="truncate">{collection.name}</span>
                    {collection.count !== undefined && (
                      <span className="ml-auto text-xs text-gray-400">{collection.count}</span>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
