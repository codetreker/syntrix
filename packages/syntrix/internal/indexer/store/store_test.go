package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectionOwnershipAndAtomicValidation(t *testing.T) {
	ref := QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: "fields-v2", Generation: "build"}
	input := Projection{Index: ref, DocumentID: "doc", PostingKeys: [][]byte{[]byte("a/doc"), []byte("a/doc")}}
	owned, err := OwnProjections([]Projection{input})
	require.NoError(t, err)
	input.PostingKeys[0][0] = 'z'
	require.Equal(t, [][]byte{[]byte("a/doc")}, owned[0].PostingKeys)
	for _, invalid := range []Projection{
		{Index: ref},
		{DocumentID: "other"},
		{Index: ref, DocumentID: "other", PostingKeys: [][]byte{nil}},
		input,
	} {
		result, err := OwnProjections([]Projection{input, invalid})
		require.Error(t, err)
		require.Nil(t, result)
	}
}

func TestReadBudgetDefaultsAndInvalidLimits(t *testing.T) {
	budget, err := NormalizeReadBudget(ReadBudget{})
	require.NoError(t, err)
	require.Equal(t, ReadBudget{MaxOverlayReplacements: 10_000, MaxOverlayBytes: 16 << 20, MaxExamined: 100_000}, budget)
	for _, invalid := range []ReadBudget{{MaxOverlayReplacements: -1}, {MaxOverlayBytes: -1}, {MaxExamined: -1}} {
		_, err := NormalizeReadBudget(invalid)
		require.Error(t, err)
	}
}

func TestBootstrapCatalogAdmissionAndGenerationAuthority(t *testing.T) {
	for _, invalid := range []BootstrapCatalog{{}, {Database: "db"}, {Database: "db", Generation: "build", TemplateFingerprints: []string{""}}} {
		_, err := OwnBootstrapCatalog(invalid)
		require.Error(t, err)
	}
	catalog, err := OwnBootstrapCatalog(BootstrapCatalog{Database: "db", Generation: "build", TemplateFingerprints: []string{"b", "a", "b"}, BootstrapProgress: "boundary"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, catalog.TemplateFingerprints)
	local := Generation{ID: "partial", Ready: true}
	g, ok := ResolveGeneration(BootstrapCatalog{}, false, "a", local, true)
	require.True(t, ok)
	require.Equal(t, local, g)
	_, ok = ResolveGeneration(catalog, true, "removed", local, true)
	require.False(t, ok)
	g, ok = ResolveGeneration(catalog, true, "a", Generation{ID: "build", Failure: "failed"}, true)
	require.True(t, ok)
	require.Equal(t, Generation{ID: "build", BootstrapProgress: "boundary", Failure: "failed"}, g)
	g, ok = ResolveGeneration(catalog, true, "a", Generation{ID: "old", Failure: "failed"}, true)
	require.True(t, ok)
	require.Equal(t, Generation{ID: "build", BootstrapProgress: "boundary", Ready: true}, g)
}

func TestCompleteCatalogInventoryValidation(t *testing.T) {
	base := BootstrapCatalog{Database: "a", Generation: "build", TemplateFingerprints: []string{"fp"}, BootstrapProgress: "boundary"}
	other := base
	other.Database = "b"
	other.TemplateFingerprints = nil
	owned, err := OwnBootstrapCatalogs([]BootstrapCatalog{base, other}, "boundary")
	require.NoError(t, err)
	require.Len(t, owned, 2)
	wrongGeneration, wrongBoundary, noTemplates, malformed := other, other, other, other
	wrongGeneration.Generation = "different"
	wrongBoundary.BootstrapProgress = "different"
	malformed.Database = ""
	for _, invalid := range [][]BootstrapCatalog{nil, {base, base}, {base, wrongGeneration}, {base, wrongBoundary}, {base, malformed}, {noTemplates}} {
		_, err := OwnBootstrapCatalogs(invalid, "boundary")
		require.Error(t, err)
	}
}
