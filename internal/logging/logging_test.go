package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewWritesECSFields(t *testing.T) {
	var out bytes.Buffer
	New(&out).Info("started", "port", 8082)

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("log is not JSON: %v (%s)", err, out.String())
	}
	for key, want := range map[string]any{
		"message":      "started",
		"log.level":    "INFO",
		"service.name": ServiceName,
		"port":         float64(8082),
	} {
		if record[key] != want {
			t.Errorf("%s = %v, want %v", key, record[key], want)
		}
	}
	if _, ok := record["@timestamp"]; !ok {
		t.Errorf("no @timestamp in %v", record)
	}
}

func TestNewKeepsGroupedKeys(t *testing.T) {
	var out bytes.Buffer
	New(&out).WithGroup("http").Info("request", "time", "12ms")

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	group, ok := record["http"].(map[string]any)
	if !ok || group["time"] != "12ms" {
		t.Errorf("grouped attribute renamed or lost: %v", record)
	}
}
