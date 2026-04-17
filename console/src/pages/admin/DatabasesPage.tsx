import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, RefreshCw, Search, Trash2, HardDrive } from 'lucide-react';
import { Button, Input, Textarea, Modal, ConfirmModal, Spinner, useToast } from '../../components/ui';
import { adminApi, type DatabaseSummary, type CreateDatabaseReq } from '../../lib/admin';

const SLUG_REGEX = /^[a-z0-9][a-z0-9-]*$/;

function statusBadge(status: string) {
  const colors: Record<string, string> = {
    active: 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300',
    deleting: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-300',
    error: 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300',
  };
  return (
    <span className={`inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium ${colors[status] || 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-300'}`}>
      {status}
    </span>
  );
}

export default function DatabasesPage() {
  const navigate = useNavigate();
  const toast = useToast();

  const [databases, setDatabases] = useState<DatabaseSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');

  // Create modal state
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<CreateDatabaseReq>({ display_name: '' });
  const [formErrors, setFormErrors] = useState<Record<string, string>>({});

  // Delete modal state
  const [deleteTarget, setDeleteTarget] = useState<DatabaseSummary | null>(null);
  const [deleting, setDeleting] = useState(false);

  const fetchDatabases = useCallback(async () => {
    try {
      const resp = await adminApi.listDatabases();
      setDatabases(resp.databases || []);
      setTotal(resp.total);
    } catch {
      toast.error('Failed to load databases');
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    fetchDatabases();
    const interval = setInterval(fetchDatabases, 60000);
    return () => clearInterval(interval);
  }, [fetchDatabases]);

  const filtered = databases.filter((db) =>
    db.display_name.toLowerCase().includes(search.toLowerCase())
  );

  // Create
  const validateForm = (): boolean => {
    const errors: Record<string, string> = {};
    if (!form.display_name.trim()) {
      errors.display_name = 'Display name is required';
    }
    if (form.slug && !SLUG_REGEX.test(form.slug)) {
      errors.slug = 'Slug must be lowercase alphanumeric and hyphens, starting with alphanumeric';
    }
    setFormErrors(errors);
    return Object.keys(errors).length === 0;
  };

  const handleCreate = async () => {
    if (!validateForm()) return;
    setCreating(true);
    try {
      await adminApi.createDatabase(form);
      toast.success('Database created');
      setCreateOpen(false);
      setForm({ display_name: '' });
      setFormErrors({});
      fetchDatabases();
    } catch {
      toast.error('Failed to create database');
    } finally {
      setCreating(false);
    }
  };

  // Delete
  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await adminApi.deleteDatabase(`id:${deleteTarget.id}`);
      toast.success(`Database "${deleteTarget.display_name}" deleted`);
      setDeleteTarget(null);
      fetchDatabases();
    } catch {
      toast.error('Failed to delete database');
    } finally {
      setDeleting(false);
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <Spinner size="lg" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 dark:text-white">Databases</h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">{total} database{total !== 1 ? 's' : ''}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => { setLoading(true); fetchDatabases(); }}>
            <RefreshCw className="w-4 h-4" />
          </Button>
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="w-4 h-4 mr-1" /> Create Database
          </Button>
        </div>
      </div>

      {/* Search */}
      <div className="relative max-w-sm">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
        <input
          type="text"
          placeholder="Search databases..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="block w-full pl-9 pr-3 py-2 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-sm text-gray-900 dark:text-gray-100 placeholder-gray-400 focus:border-blue-500 focus:ring-blue-500"
        />
      </div>

      {/* Grid */}
      {filtered.length === 0 ? (
        <div className="text-center py-12">
          <HardDrive className="w-12 h-12 text-gray-400 mx-auto mb-3" />
          <p className="text-gray-500 dark:text-gray-400">
            {search ? 'No databases match your search' : 'No databases yet'}
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {filtered.map((db) => (
            <div
              key={db.id}
              onClick={() => navigate('/collections')}
              className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-4 hover:border-blue-400 dark:hover:border-blue-500 cursor-pointer transition-colors"
            >
              <div className="flex items-start justify-between mb-2">
                <h3 className="font-semibold text-gray-900 dark:text-white truncate">{db.display_name}</h3>
                {statusBadge(db.status)}
              </div>
              {db.slug && (
                <p className="text-xs text-gray-500 dark:text-gray-400 font-mono mb-1">{db.slug}</p>
              )}
              <p className="text-xs text-gray-500 dark:text-gray-400 mb-1">Owner: {db.owner_id}</p>
              <p className="text-xs text-gray-500 dark:text-gray-400 mb-3">
                Created: {new Date(db.created_at).toLocaleDateString()}
              </p>
              <div className="flex justify-end">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation();
                    setDeleteTarget(db);
                  }}
                >
                  <Trash2 className="w-4 h-4 text-red-500" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create Modal */}
      <Modal
        isOpen={createOpen}
        onClose={() => { setCreateOpen(false); setFormErrors({}); }}
        title="Create Database"
        size="md"
        footer={
          <>
            <Button variant="secondary" onClick={() => { setCreateOpen(false); setFormErrors({}); }}>
              Cancel
            </Button>
            <Button onClick={handleCreate} loading={creating}>
              Create
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Input
            label="Display Name"
            required
            value={form.display_name}
            onChange={(e) => setForm({ ...form, display_name: e.target.value })}
            error={formErrors.display_name}
            placeholder="My Database"
          />
          <Input
            label="Slug"
            value={form.slug || ''}
            onChange={(e) => setForm({ ...form, slug: e.target.value || undefined })}
            error={formErrors.slug}
            placeholder="my-database"
            helperText="Lowercase letters, numbers, and hyphens"
          />
          <Textarea
            label="Description"
            value={form.description || ''}
            onChange={(e) => setForm({ ...form, description: e.target.value || undefined })}
            placeholder="Optional description"
            rows={3}
          />
        </div>
      </Modal>

      {/* Delete Confirm */}
      <ConfirmModal
        isOpen={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={handleDelete}
        title="Delete Database"
        message={`Are you sure you want to delete "${deleteTarget?.display_name}"? This action cannot be undone.`}
        confirmText="Delete"
        variant="danger"
        loading={deleting}
      />
    </div>
  );
}
