package scenario

import (
	"errors"
	"testing"
)

func TestEvalJSONPath(t *testing.T) {
	body := `{"id": 42, "name": "test", "active": true, "score": 3.14, "data": {"name": "nested", "count": 7}, "list": [1,2,3]}`

	tests := []struct {
		name    string
		body    string
		path    string
		want    string
		wantErr error
	}{
		{
			name: "integer value",
			body: body,
			path: "$.id",
			want: "42",
		},
		{
			name: "string value",
			body: body,
			path: "$.name",
			want: "test",
		},
		{
			name: "boolean value",
			body: body,
			path: "$.active",
			want: "true",
		},
		{
			name: "float value",
			body: body,
			path: "$.score",
			want: "3.14",
		},
		{
			name: "nested extraction",
			body: body,
			path: "$.data.name",
			want: "nested",
		},
		{
			name: "nested integer",
			body: body,
			path: "$.data.count",
			want: "7",
		},
		{
			name:    "invalid path no dollar dot",
			body:    body,
			path:    "id",
			wantErr: errInvalidJSONPath,
		},
		{
			name:    "missing key",
			body:    body,
			path:    "$.missing",
			wantErr: errJSONPathNotFound,
		},
		{
			name:    "non-object intermediate",
			body:    body,
			path:    "$.name.sub",
			wantErr: errJSONPathNotObject,
		},
		{
			name:    "non-scalar leaf",
			body:    body,
			path:    "$.list",
			wantErr: errJSONPathNotScalar,
		},
		{
			name:    "root is array not object",
			body:    `[1, 2, 3]`,
			path:    "$.foo",
			wantErr: errJSONPathNotObject,
		},
		{
			name:    "root is string",
			body:    `"hello"`,
			path:    "$.foo",
			wantErr: errJSONPathNotObject,
		},
		{
			name: "null value at path",
			body: `{"id": null}`,
			path: "$.id",
			want: "null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evalJSONPath(tt.body, tt.path)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
