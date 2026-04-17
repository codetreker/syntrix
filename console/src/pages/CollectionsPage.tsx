import { useState } from 'react';
import { Plus } from 'lucide-react';
import { 
  CollectionTree, 
  DocumentList, 
  DocumentViewer,
  DocumentEditor 
} from '../components/features/data-browser';
import { ConfirmModal, Button, useToast } from '../components/ui';
import type { Document } from '../lib/documents';
import { documentsApi } from '../lib/documents';

export default function CollectionsPage() {
  const { success, error } = useToast();
  const [selectedDatabase, setSelectedDatabase] = useState<string | null>(null);
  const [selectedCollection, setSelectedCollection] = useState<string | null>(null);
  const [selectedDocument, setSelectedDocument] = useState<Document | null>(null);
  const [editingDocument, setEditingDocument] = useState<Document | null | 'new'>(null);
  const [deletingDocument, setDeletingDocument] = useState<Document | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);

  const handleSelectDocument = (doc: Document) => {
    setSelectedDocument(doc);
  };

  const handleCloseViewer = () => {
    setSelectedDocument(null);
  };

  const handleEditDocument = (doc: Document) => {
    setEditingDocument(doc);
  };

  const handleCreateDocument = () => {
    if (selectedCollection) {
      setEditingDocument('new');
    }
  };

  const handleSaveDocument = () => {
    setEditingDocument(null);
    setRefreshKey((k) => k + 1);
    success('Document saved successfully');
  };

  const handleDeleteDocument = (doc: Document) => {
    setDeletingDocument(doc);
  };

  const confirmDelete = async () => {
    if (!deletingDocument || !selectedCollection) return;
    
    try {
      await documentsApi.delete(selectedDatabase!, selectedCollection, deletingDocument.id);
      success('Document deleted successfully');
      setDeletingDocument(null);
      setSelectedDocument(null);
      setRefreshKey((k) => k + 1);
    } catch (err) {
      error(err instanceof Error ? err.message : 'Failed to delete document');
    }
  };

  return (
    <div className="h-full flex flex-col -m-6">
      {/* Toolbar */}
      <div className="flex items-center justify-between px-6 py-3 border-b border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800">
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">Data Browser</h2>
        {selectedCollection && (
          <Button variant="primary" size="sm" onClick={handleCreateDocument}>
            <Plus className="w-4 h-4 mr-1.5" />
            New Document
          </Button>
        )}
      </div>

      {/* Main Content */}
      <div className="flex-1 flex overflow-hidden">
        {/* Collection Tree */}
        <CollectionTree
          selectedDatabase={selectedDatabase}
          selectedCollection={selectedCollection}
          onSelectDatabase={setSelectedDatabase}
          onSelectCollection={setSelectedCollection}
        />

        {/* Document List */}
        <div className="flex-1 bg-white dark:bg-gray-900 overflow-hidden">
          {selectedCollection ? (
            <DocumentList
              key={`${selectedCollection}-${refreshKey}`}
              database={selectedDatabase!}
              collection={selectedCollection}
              selectedDocumentId={selectedDocument?.id || null}
              onSelectDocument={handleSelectDocument}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-gray-500">
              Select a collection to browse documents
            </div>
          )}
        </div>

        {/* Document Viewer */}
        {selectedDocument && (
          <DocumentViewer
            document={selectedDocument}
            onClose={handleCloseViewer}
            onEdit={handleEditDocument}
            onDelete={handleDeleteDocument}
          />
        )}
      </div>

      {/* Document Editor Modal */}
      {editingDocument && selectedCollection && (
        <DocumentEditor
          document={editingDocument === 'new' ? null : editingDocument}
          database={selectedDatabase!}
          collection={selectedCollection}
          onClose={() => setEditingDocument(null)}
          onSave={handleSaveDocument}
        />
      )}

      {/* Delete Confirmation */}
      <ConfirmModal
        isOpen={deletingDocument !== null}
        onClose={() => setDeletingDocument(null)}
        onConfirm={confirmDelete}
        title="Delete Document"
        message={`Are you sure you want to delete "${deletingDocument?.id}"? This action cannot be undone.`}
        confirmText="Delete"
        variant="danger"
      />
    </div>
  );
}
