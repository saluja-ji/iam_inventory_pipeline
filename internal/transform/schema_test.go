package transform

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The committed schemas.json drives the Glue tables; regenerate with `make schema`.
func TestGlueSchemaFileUpToDate(t *testing.T) {
	b, err := os.ReadFile("../../terraform/modules/glue/schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string][]Column
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Schemas()) {
		t.Fatal("terraform/modules/glue/schemas.json is stale; run `make schema`")
	}
}

func TestSchemaTypes(t *testing.T) {
	cols := Schemas()[TRoles]
	want := map[string]string{"create_date": "timestamp", "last_used_date": "timestamp", "managed_policy_count": "int", "role_name": "string"}
	for _, c := range cols {
		if w, ok := want[c.Name]; ok && w != c.Type {
			t.Errorf("%s: got %s want %s", c.Name, c.Type, w)
		}
	}
}
