package observability

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func assertHasAttr(t *testing.T, attrs []attribute.KeyValue, key string) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			return
		}
	}
	t.Errorf("expected attribute %q not found", key)
}

func assertAttrInt(t *testing.T, attrs []attribute.KeyValue, key string, want int) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			got := int(a.Value.AsInt64())
			if got != want {
				t.Errorf("attribute %q: got %d, want %d", key, got, want)
			}
			return
		}
	}
	t.Errorf("expected attribute %q not found", key)
}

func assertAttrFloat64(t *testing.T, attrs []attribute.KeyValue, key string, want float64) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			got := a.Value.AsFloat64()
			if got != want {
				t.Errorf("attribute %q: got %f, want %f", key, got, want)
			}
			return
		}
	}
	t.Errorf("expected attribute %q not found", key)
}

func assertAttrString(t *testing.T, attrs []attribute.KeyValue, key, want string) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			got := a.Value.AsString()
			if got != want {
				t.Errorf("attribute %q: got %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Errorf("expected attribute %q not found", key)
}

func assertAttrBool(t *testing.T, attrs []attribute.KeyValue, key string, want bool) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			got := a.Value.AsBool()
			if got != want {
				t.Errorf("attribute %q: got %v, want %v", key, got, want)
			}
			return
		}
	}
	t.Errorf("expected attribute %q not found", key)
}
