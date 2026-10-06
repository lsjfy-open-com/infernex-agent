package domain

import "fmt"

// AuthorizeScope is the deliberately small PR1 authorization primitive. The
// caller must obtain principalScope from trusted process/authentication state.
func AuthorizeScope(principalScope, recordScope string) error {
	if principalScope == "" || recordScope == "" || principalScope != recordScope {
		return fmt.Errorf("domain scope is not authorized")
	}
	return nil
}

func sameRecordRef(a, b RecordRef) bool {
	return a.TenantScope == b.TenantScope && a.Kind == b.Kind && a.ID == b.ID && a.Revision == b.Revision
}

func sameEntityRef(a, b EntityRef) bool {
	return sameRecordRef(a.SnapshotRef, b.SnapshotRef) && a.EntityID == b.EntityID
}
