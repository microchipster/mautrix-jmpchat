package stanza

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestNodeUnmarshalRejectsDeepXML(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < xmlElementMaxDepth; i++ {
		builder.WriteString("<x>")
	}
	for i := 0; i < xmlElementMaxDepth; i++ {
		builder.WriteString("</x>")
	}

	var node Node
	if err := xml.Unmarshal([]byte(builder.String()), &node); err == nil {
		t.Fatal("expected deep XML to fail")
	}
}
