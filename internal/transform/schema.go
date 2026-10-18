package transform

import (
	"strings"

	"github.com/parquet-go/parquet-go"
)

// Column is a Glue/Athena column definition.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Schemas returns the Glue column list per table, derived from the Parquet row structs
// so Terraform can never drift from what the Lambda writes.
func Schemas() map[string][]Column {
	return map[string][]Column{
		TRoles:       columns(parquet.SchemaOf(RoleRow{})),
		TUsers:       columns(parquet.SchemaOf(UserRow{})),
		TGroups:      columns(parquet.SchemaOf(GroupRow{})),
		TPolicies:    columns(parquet.SchemaOf(PolicyRow{})),
		TAttachments: columns(parquet.SchemaOf(RoleAttachmentRow{})),
		TTrust:       columns(parquet.SchemaOf(TrustRow{})),
		TAccessKeys:  columns(parquet.SchemaOf(AccessKeyRow{})),
		TFindings:    columns(parquet.SchemaOf(FindingRow{})),
	}
}

func columns(s *parquet.Schema) []Column {
	var out []Column
	for _, f := range s.Fields() {
		out = append(out, Column{Name: f.Name(), Type: glueType(f)})
	}
	return out
}

func glueType(f parquet.Field) string {
	t := f.Type()
	switch t.Kind() {
	case parquet.Boolean:
		return "boolean"
	case parquet.Int32:
		return "int"
	case parquet.Int64:
		if lt := t.LogicalType(); lt != nil && strings.HasPrefix(lt.String(), "TIMESTAMP") {
			return "timestamp"
		}
		return "bigint"
	default:
		return "string"
	}
}
