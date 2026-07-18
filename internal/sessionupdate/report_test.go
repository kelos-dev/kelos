package sessionupdate

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestReportRoundTrip(t *testing.T) {
	report := Report{
		RequestID: "request",
		PodUID:    types.UID("pod-uid"),
		Phase:     PhaseDrained,
	}
	encoded, err := EncodeReport(report)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReport(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, report) {
		t.Fatalf("decoded report = %#v, want %#v", decoded, report)
	}
}

func TestDecodeReportRejectsInvalidReport(t *testing.T) {
	tests := map[string]string{
		"missing pod UID": `{"requestID":"request","phase":"Drained"}`,
		"invalid phase":   `{"requestID":"request","podUID":"pod-uid","phase":"Unknown"}`,
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeReport(value); err == nil {
				t.Fatal("DecodeReport() accepted an invalid report")
			}
		})
	}
}
