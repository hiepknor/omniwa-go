package projection_model

import "testing"

func TestContactProjectionTableNamesAndIdentityKinds(t *testing.T) {
	if (Contact{}).TableName() != "projected_contacts" {
		t.Fatalf("contact table = %q", (Contact{}).TableName())
	}
	if (ContactIdentity{}).TableName() != "projected_contact_identities" {
		t.Fatalf("contact identity table = %q", (ContactIdentity{}).TableName())
	}
	kinds := []ContactIdentityKind{ContactIdentityKindJID, ContactIdentityKindPhoneJID, ContactIdentityKindLID, ContactIdentityKindUsername}
	seen := make(map[ContactIdentityKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if kind == "" {
			t.Fatal("empty contact identity kind")
		}
		if _, duplicate := seen[kind]; duplicate {
			t.Fatalf("duplicate contact identity kind %q", kind)
		}
		seen[kind] = struct{}{}
	}
}
