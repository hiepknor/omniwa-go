package instance_model

import (
	"encoding/json"
	"testing"
)

func TestPersistenceInstanceCannotBeSerialized(t *testing.T) {
	value, err := json.Marshal(Instance{
		Id: "instance-id", Name: "name", Token: "bearer-secret", Proxy: `{"password":"proxy-secret"}`, Qrcode: "pairing-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "{}" {
		t.Fatalf("persistence record serialized as %s", value)
	}
}
