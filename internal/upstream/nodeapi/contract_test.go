package nodeapi

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNodeV1ContractFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/node_v1_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		ProtocolVersion string      `json:"protocol_version"`
		Routes          [][3]string `json:"routes"`
		ErrorFields     []string    `json:"error_fields"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.ProtocolVersion != "h3-node-v1" || len(contract.Routes) != 12 {
		t.Fatalf("contract = %+v", contract)
	}
	if len(contract.ErrorFields) != 5 {
		t.Fatalf("error fields = %v", contract.ErrorFields)
	}
}

func TestRuntimeDoesNotImportDocs(t *testing.T) {
	for _, source := range []string{"client.go", "types.go"} {
		data, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if containsDocsPath(string(data)) {
			t.Fatalf("%s references docs runtime path", source)
		}
	}
}

func containsDocsPath(source string) bool {
	for index := 0; index+5 <= len(source); index++ {
		if source[index:index+5] == "docs/" || source[index:index+5] == "docs\\" {
			return true
		}
	}
	return false
}
