package adroute

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCursorInstructionsAreRewrittenOnlyWhenEnabled(t *testing.T) {
	body := map[string]any{"instructions": "Before. You operate in Cursor. After.", "input": "You operate in Cursor."}
	unchanged := prepareAdapterRequest(body, nil)
	if unchanged["instructions"] != body["instructions"] {
		t.Fatal("unbound request was changed")
	}
	pack := &AdapterPackage{Options: AdapterOptions{NeutralizeCursorInstructions: true}}
	got := prepareAdapterRequest(body, pack)
	if got["instructions"] != "Before. You operate in an editor. After." {
		t.Fatalf("unexpected instructions: %v", got["instructions"])
	}
	if got["input"] != body["input"] || body["instructions"] != "Before. You operate in Cursor. After." {
		t.Fatal("unrelated input or original request was modified")
	}
	pack.Options.NeutralizeCursorInstructions = false
	if prepareAdapterRequest(body, pack)["instructions"] != body["instructions"] {
		t.Fatal("disabled option changed request")
	}
}

func TestCursorAdapterPackageVerifies(t *testing.T) {
	data, err := os.ReadFile("../../examples/pixel-swe-cursor.adroute-adapter.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack AdapterPackage
	if err := json.Unmarshal(data, &pack); err != nil {
		t.Fatal(err)
	}
	if !pack.Options.NeutralizeCursorInstructions {
		t.Fatal("cursor request adaptation is disabled")
	}
	result, err := VerifyPackage(pack)
	if err != nil || !result.ConfigurationValid || !result.ReplayPassed {
		t.Fatalf("adapter verification: %+v, %v", result, err)
	}
}
