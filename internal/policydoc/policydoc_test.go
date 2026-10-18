package policydoc

import "testing"

func TestParseShapes(t *testing.T) {
	doc, err := Parse([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Statements) != 1 || !doc.Statements[0].MatchesAction("s3:GetObject") || !doc.Statements[0].AllResources() {
		t.Fatalf("unexpected parse: %+v", doc)
	}

	doc, err = Parse([]byte(`{"Statement":[{"Effect":"Allow","Principal":"*","Action":["sts:AssumeRole"],"Condition":{"StringEquals":{"sts:ExternalId":"x"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := doc.Statements[0]
	if s.Principal["AWS"][0] != "*" || !s.HasCondition {
		t.Fatalf("principal/condition not parsed: %+v", s)
	}
}

func TestMatchesActionCaseInsensitive(t *testing.T) {
	s := Statement{Actions: []string{"IAM:Pass*"}}
	if !s.MatchesAction("iam:PassRole") || s.MatchesAction("iam:GetRole") {
		t.Fatal("wildcard match wrong")
	}
}

func TestParseInvalid(t *testing.T) {
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("expected error")
	}
}
