import { useState, useEffect, useCallback, useRef } from 'react';
import { ChevronLeft, ChevronRight, RefreshCw, Search, X } from 'lucide-react';
import { documentsApi, formatDocumentJson, formatDocumentDate, parseFilterValue, type Document, type QueryResponse, type Filter } from '../../../lib/documents';
import { Table, type Column, Button, Spinner } from '../../ui';

const FILTER_OPS: { value: Filter['op']; label: string }[] = [
  { value: '==', label: '=' },
  { value: '!=', label: '!=' },
  { value: '>', label: '>' },
  { value: '>=', label: '>=' },
  { value: '<', label: '<' },
  { value: '<=', label: '<=' },
  { value: 'contains', label: 'contains' },
];

interface DocumentListProps {
  database: string;
  collection: string;
  selectedDocumentId?: string | null;
  onSelectDocument: (doc: Document) => void;
}

export function DocumentList({ database, collection, onSelectDocument }: DocumentListProps) {
  const [documents, setDocuments] = useState<Document[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const hasMore = nextCursor !== null;
  const [cursors, setCursors] = useState<(string | undefined)[]>([]);
  const [currentCursor, setCurrentCursor] = useState<string | undefined>(undefined);
  const [filterField, setFilterField] = useState('');
  const [filterOp, setFilterOp] = useState<Filter['op']>('==');
  const [filterValue, setFilterValue] = useState('');
  const [filterError, setFilterError] = useState<string | null>(null);
  const [activeFilters, setActiveFilters] = useState<Filter[]>([]);
  const requestVersion = useRef(0);
  const limit = 20;

  const fetchDocuments = useCallback(async (startAfter?: string, isGoingBack = false) => {
    const version = ++requestVersion.current;
    setLoading(true);
    setError(null);
    if (startAfter === undefined && !isGoingBack) {
      setNextCursor(null);
      setCurrentCursor(undefined);
      setCursors([]);
    }
    try {
      const response: QueryResponse = await documentsApi.query({
        database,
        collection,
        filters: activeFilters,
        limit,
        startAfter,
      });
      if (requestVersion.current !== version) return;
      setDocuments(response.documents);
      setNextCursor(response.nextCursor);
      setCurrentCursor(startAfter);
    } catch (err) {
      if (requestVersion.current !== version) return;
      setError(err instanceof Error ? err.message : 'Failed to load documents');
      setDocuments([]);
      setNextCursor(null);
    } finally {
      if (requestVersion.current === version) setLoading(false);
    }
  }, [database, collection, activeFilters]);

  useEffect(() => {
    void fetchDocuments();
    return () => { requestVersion.current++; };
  }, [collection, fetchDocuments]);

  const handleAddFilter = () => {
    if (!filterField.trim()) return;
    try {
      const value = parseFilterValue(filterValue);
      setActiveFilters((prev) => [...prev, { field: filterField, op: filterOp, value }]);
      setFilterError(null);
    } catch (err) {
      setFilterError(err instanceof Error ? err.message : 'Invalid filter value');
      return;
    }
    setFilterField('');
    setFilterValue('');
  };

  const handleRemoveFilter = (index: number) => {
    setActiveFilters((prev) => prev.filter((_, i) => i !== index));
  };

  const handleNextPage = () => {
    if (nextCursor !== null) {
      setCursors(prev => [...prev, currentCursor]);
      fetchDocuments(nextCursor);
    }
  };

  const handlePrevPage = () => {
    if (cursors.length > 0) {
      const newCursors = [...cursors];
      const prevCursor = newCursors.pop();
      setCursors(newCursors);
      fetchDocuments(prevCursor, true);
    }
  };

  const handleRefresh = () => {
    setCursors([]);
    setCurrentCursor(undefined);
    fetchDocuments();
  };

  const columns: Column<Document>[] = [
    {
      key: 'id',
      header: 'ID',
      width: '200px',
      render: (doc) => (
        <span className="font-mono text-xs truncate block max-w-[180px]" title={doc.id}>
          {doc.id}
        </span>
      ),
    },
    {
      key: 'preview',
      header: 'Preview',
      render: (doc) => {
        const preview = getDocumentPreview(doc);
        return (
          <span className="text-gray-600 dark:text-gray-400 truncate block max-w-[300px]" title={preview}>
            {preview}
          </span>
        );
      },
    },
    {
      key: 'createdAt',
      header: 'Created',
      width: '150px',
      render: (doc) => {
        // Support both createdAt (backend) and _createdAt (legacy)
        const createdAt = doc.createdAt || doc._createdAt;
        return (
          <span className="text-xs text-gray-500">
            {createdAt ? formatDocumentDate(createdAt) : '-'}
          </span>
        );
      },
    },
  ];

  const currentPage = cursors.length + 1;
  const canGoPrev = cursors.length > 0;
  const canGoNext = hasMore;

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-center">
        <p className="text-red-600 dark:text-red-400 mb-4">{error}</p>
        <Button variant="secondary" onClick={handleRefresh}>
          <RefreshCw className="w-4 h-4 mr-2" />
          Retry
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
        <div className="flex items-center gap-2">
          <span className="font-medium text-gray-900 dark:text-white">{collection}</span>
          {loading && <Spinner size="sm" />}
        </div>
        <button
          onClick={handleRefresh}
          disabled={loading}
          className="p-1.5 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 disabled:opacity-50"
          title="Refresh"
        >
          <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
        </button>
      </div>

      {/* Filter Bar */}
      <div className="px-4 py-2 border-b border-gray-200 dark:border-gray-700 space-y-2">
        <div className="flex items-center gap-2">
          <input
            type="text"
            placeholder="Field name"
            value={filterField}
            onChange={(e) => setFilterField(e.target.value)}
            className="w-32 px-2 py-1 text-sm rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
          />
          <select
            value={filterOp}
            onChange={(e) => setFilterOp(e.target.value as Filter['op'])}
            className="px-2 py-1 text-sm rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
          >
            {FILTER_OPS.map((o) => (
              <option key={o.value} value={o.value}>{o.label}</option>
            ))}
          </select>
          <input
            type="text"
            placeholder="Value"
            value={filterValue}
            onChange={(e) => setFilterValue(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && handleAddFilter()}
            className="w-32 px-2 py-1 text-sm rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
          />
          <button
            onClick={handleAddFilter}
            disabled={!filterField.trim()}
            className="p-1.5 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-600 dark:text-gray-400 disabled:opacity-50"
            title="Add filter"
          >
            <Search className="w-4 h-4" />
          </button>
        </div>
        {filterError && <p className="text-xs text-red-600 dark:text-red-400">{filterError}</p>}
        {activeFilters.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {activeFilters.map((f, i) => (
              <span
                key={i}
                className="inline-flex items-center gap-1 px-2 py-0.5 text-xs rounded-full bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300"
              >
                {f.field} {FILTER_OPS.find((o) => o.value === f.op)?.label} {String(f.value)}
                <button onClick={() => handleRemoveFilter(i)} className="hover:text-blue-900 dark:hover:text-blue-100">
                  <X className="w-3 h-3" />
                </button>
              </span>
            ))}
          </div>
        )}
      </div>

      {/* Table */}
      <div className="flex-1 overflow-auto">
        <Table
          columns={columns}
          data={documents}
          loading={loading && documents.length === 0}
          emptyMessage={`No documents in ${collection}`}
          onRowClick={onSelectDocument}
          rowKey={(doc) => doc.id}
        />
      </div>

      {/* Pagination */}
      <div className="flex items-center justify-between px-4 py-3 border-t border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800/50">
        <span className="text-sm text-gray-500">
          {documents.length > 0 
            ? `${documents.length} documents${hasMore ? '+' : ''}`
            : '0 documents'
          }
        </span>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={handlePrevPage}
            disabled={!canGoPrev || loading}
          >
            <ChevronLeft className="w-4 h-4" />
          </Button>
          <span className="text-sm text-gray-600 dark:text-gray-400">
            Page {currentPage}
          </span>
          <Button
            variant="ghost"
            size="sm"
            onClick={handleNextPage}
            disabled={!canGoNext || loading}
          >
            <ChevronRight className="w-4 h-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}

// Helper functions
function getDocumentPreview(doc: Document): string {
  // Priority: text field first, then other fields
  if (doc.text && typeof doc.text === 'string') {
    return doc.text.length > 60 ? doc.text.slice(0, 60) + '...' : doc.text;
  }
  
  const excluded = ['id', 'collection', '_collection', 'createdAt', '_createdAt', 'updatedAt', '_updatedAt', 'version'];
  const entries = Object.entries(doc).filter(([key]) => !excluded.includes(key));
  
  if (entries.length === 0) return '{}';
  
  const [key, value] = entries[0];
  if (typeof value === 'string') {
    return value.length > 50 ? value.slice(0, 50) + '...' : value;
  }
  return `${key}: ${formatDocumentJson(value, 0)}`.slice(0, 60);
}
