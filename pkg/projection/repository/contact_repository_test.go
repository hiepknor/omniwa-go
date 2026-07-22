package projection_repository

import (
	"encoding/json"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestValidateContactPatchNormalizesAliasesAndRequiresPreferredJID(t *testing.T) {
	patch := ContactPatch{
		InstanceID: "instance-a", Aspect: ContactAspectDetails, OccurredAt: time.Unix(100, 0), EventKey: "contact-100",
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindJID, Value: " contact@s.whatsapp.net "},
			{Kind: projection_model.ContactIdentityKindJID, Value: "contact@s.whatsapp.net"},
		},
	}
	identities, err := validateContactPatch(patch)
	if err != nil || len(identities) != 1 || identities[0].Value != "contact@s.whatsapp.net" {
		t.Fatalf("normalized identities = %#v, %v", identities, err)
	}
	patch.Identities = []ContactIdentityRef{{Kind: projection_model.ContactIdentityKindUsername, Value: "contact"}}
	if _, err := validateContactPatch(patch); err == nil {
		t.Fatal("username-only patch without preferred JID was accepted")
	}
}

func TestContactFieldVersionsAreIndependent(t *testing.T) {
	versions := contactFieldVersions{
		ContactAspectPushName: {OccurredAt: time.Unix(300, 0), EventKey: "push-300"},
	}
	raw, err := json.Marshal(versions)
	if err != nil {
		t.Fatal(err)
	}
	contact := projection_model.Contact{SourceOccurredAt: time.Unix(300, 0), SourceEventKey: "push-300", FieldVersions: raw}
	decoded, err := decodeContactVersions(contact.FieldVersions)
	if err != nil {
		t.Fatal(err)
	}
	if details := contactVersionFor(contact, decoded, ContactAspectDetails); !details.OccurredAt.IsZero() || details.EventKey != "" {
		t.Fatalf("missing details aspect inherited unrelated version: %#v", details)
	}
	if push := contactVersionFor(contact, decoded, ContactAspectPushName); !push.OccurredAt.Equal(time.Unix(300, 0)) {
		t.Fatalf("push-name version = %#v", push)
	}
}

func TestApplyContactAspectOnlyChangesSelectedFieldGroup(t *testing.T) {
	oldName, oldPush := "Old name", "Old push"
	contact := projection_model.Contact{FullName: &oldName, PushName: &oldPush}
	newName := "New name"
	applyContactAspect(&contact, ContactPatch{Aspect: ContactAspectDetails, FullName: &newName})
	if contact.FullName == nil || *contact.FullName != newName || contact.PushName == nil || *contact.PushName != oldPush {
		t.Fatalf("contact after details patch = %#v", contact)
	}
}

func TestEscapeContactSearchPatternTreatsWildcardsLiterally(t *testing.T) {
	if got, want := escapeContactSearchPattern(`a%b_c\d`), `a\%b\_c\\d`; got != want {
		t.Fatalf("escaped pattern = %q, want %q", got, want)
	}
}
