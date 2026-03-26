package connector

import "testing"

func TestParseGatewayAssignments(t *testing.T) {
	values, err := parseGatewayAssignments(`confirm=yes nickname="John Doe"`)
	if err != nil {
		t.Fatalf("parseGatewayAssignments returned error: %v", err)
	}
	if values["confirm"] != "yes" {
		t.Fatalf("unexpected confirm value: %q", values["confirm"])
	}
	if values["nickname"] != "John Doe" {
		t.Fatalf("unexpected nickname value: %q", values["nickname"])
	}
}

func TestBuildGatewaySubmitFormKeepsHiddenFields(t *testing.T) {
	pending := PendingGatewayForm{
		Fields: []PendingGatewayField{
			{Var: "FORM_TYPE", Type: "hidden", Values: []string{"jabber:x:data"}},
			{Var: "confirm", Type: "boolean"},
			{Var: "nickname", Type: "text-single", Values: []string{"old"}},
		},
	}
	form := buildGatewaySubmitForm(pending, map[string]string{"confirm": "yes", "nickname": "New Name"})
	if form == nil {
		t.Fatal("expected submit form")
	}
	if form.Type != "submit" {
		t.Fatalf("unexpected form type: %q", form.Type)
	}
	if len(form.Fields) != 3 {
		t.Fatalf("unexpected field count: %d", len(form.Fields))
	}
	if form.Fields[0].Var != "FORM_TYPE" || len(form.Fields[0].Values) != 1 || form.Fields[0].Values[0] != "jabber:x:data" {
		t.Fatalf("hidden field not preserved: %+v", form.Fields[0])
	}
	if form.Fields[1].Values[0] != "1" {
		t.Fatalf("boolean field was not normalized: %+v", form.Fields[1])
	}
	if form.Fields[2].Values[0] != "New Name" {
		t.Fatalf("text field not updated: %+v", form.Fields[2])
	}
}

func TestValidateGatewayAssignments(t *testing.T) {
	pending := PendingGatewayForm{
		Node: "register",
		Fields: []PendingGatewayField{
			{Var: "carrier", Required: true, Options: []PendingGatewayOption{{Label: "Rogers", Value: "rogers"}}},
			{Var: "confirm", Required: true, Type: "boolean"},
		},
	}
	values := map[string]string{"carrier": "Rogers", "confirm": "true"}
	if err := validateGatewayAssignments(pending, values); err != nil {
		t.Fatalf("validateGatewayAssignments returned error: %v", err)
	}
	if values["carrier"] != "rogers" {
		t.Fatalf("expected option label to normalize to value, got %q", values["carrier"])
	}
	if err := validateGatewayAssignments(pending, map[string]string{"confirm": "true"}); err == nil {
		t.Fatal("expected missing required field error")
	}
}
