package audit

import (
	"testing"
	"time"
)

// Every type a constructor puts in data{} is carried into the record, and a
// type the builder does not know is reported instead of vanishing.
func TestEveryTypeOfDataIsCarriedAndAnUnknownOneIsReported(t *testing.T) {
	var told []string
	defer SetUnsupportedDataHook(func(key string, _ any) { told = append(told, key) })()
	s := data{"s": "x", "b": true, "n": 3, "n64": int64(1) << 40, "l": []string{"a", "b"}, "empty": "", "none": []string(nil)}.proto()
	f := s.GetFields()
	if f["s"].GetStringValue() != "x" || !f["b"].GetBoolValue() || f["n"].GetNumberValue() != 3 || f["n64"].GetNumberValue() != float64(int64(1)<<40) ||
		len(f["l"].GetListValue().GetValues()) != 2 {
		t.Errorf("%v", s)
	}
	if _, ok := f["empty"]; ok {
		t.Error("an empty string is carried")
	}
	if _, ok := f["none"]; ok {
		t.Error("an empty list is carried")
	}
	if len(told) != 0 {
		t.Errorf("a known type was reported: %v", told)
	}
	got := data{"when": time.Unix(0, 0).UTC()}.proto()
	if len(told) != 1 || told[0] != "when" || got.GetFields()["when"].GetStringValue() == "" {
		t.Errorf("an unknown type: told %v, record %v", told, got)
	}
}
