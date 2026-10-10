package storage

import (
	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/core/storage/types"
)

// StorageFactory exposes document and catalog stores that borrow physical
// connections from Backends. Their lifetime is bounded by that owner.
type StorageFactory interface {
	Document() types.DocumentStore
	Database() database.DatabaseStore
}
