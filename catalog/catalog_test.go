package catalog

import (
	"testing"

	"shieldtunnel/domain"
)

func TestCatalogErrorCarriesCode(t *testing.T) {
	err := CatalogError(domain.CodeStaleSummary, "rules changed")
	if err.Code != domain.CodeStaleSummary {
		t.Fatalf("code %s want %s", err.Code, domain.CodeStaleSummary)
	}
	if len(err.Reasons) != 1 || err.Reasons[0].Message != "rules changed" {
		t.Fatalf("unexpected reasons: %+v", err.Reasons)
	}
}

func TestCatalogErrorIsDomainError(t *testing.T) {
	var err error = CatalogError(domain.CodeRingTypeMismatch, "x")
	de, ok := err.(*domain.Error)
	if !ok {
		t.Fatal("CatalogError should be a *domain.Error")
	}
	if de.Code != domain.CodeRingTypeMismatch {
		t.Fatalf("code %s", de.Code)
	}
}
