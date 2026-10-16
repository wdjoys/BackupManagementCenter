package logging

import (
	"bytes"
	"testing"
)

func TestSinkBuffersAndForwards(t *testing.T) {
	var output bytes.Buffer
	sink := NewSink(&output, 8)
	sink.Write([]byte("[INFO] before connect\n"))

	var got []Entry
	sink.SetHandler(func(entry Entry) error {
		got = append(got, entry)
		return nil
	})
	sink.Write([]byte("[WARN] reconnecting\n"))

	if output.String() != "[INFO] before connect\n[WARN] reconnecting\n" {
		t.Fatalf("unexpected output: %q", output.String())
	}
	if len(got) != 2 || got[0].Level != "info" || got[1].Level != "warn" {
		t.Fatalf("unexpected entries: %+v", got)
	}
	if got[0].Type != "system" || got[1].Type != "connection" {
		t.Fatalf("unexpected types: %+v", got)
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("unexpected sequence: %+v", got)
	}
}

func TestSinkQueuesAfterHandlerFailure(t *testing.T) {
	sink := NewSink(nil, 8)
	called := false
	sink.SetHandler(func(Entry) error {
		if !called {
			called = true
			return errHandler{}
		}
		return nil
	})
	sink.Write([]byte("[INFO] queued\n"))

	var got []Entry
	sink.SetHandler(func(entry Entry) error {
		got = append(got, entry)
		return nil
	})
	if len(got) != 1 || got[0].Message != "queued" {
		t.Fatalf("expected failed delivery to be queued, got %+v", got)
	}
}

type errHandler struct{}

func (errHandler) Error() string { return "send failed" }
