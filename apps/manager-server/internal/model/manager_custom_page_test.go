package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeManagerCustomPagesPreservesOrderAndEmptySlice(t *testing.T) {
	input := []ManagerCustomPageConfig{
		{ID: " status ", Title: " Status board ", URL: " https://status.example.test/overview "},
		{ID: "docs", Title: "Docs", URL: "http://docs.example.test"},
	}
	want := []ManagerCustomPageConfig{
		{ID: "status", Title: "Status board", URL: "https://status.example.test/overview"},
		{ID: "docs", Title: "Docs", URL: "http://docs.example.test"},
	}
	if got := NormalizeManagerCustomPages(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized pages = %#v, want %#v", got, want)
	}
	if got := NormalizeManagerCustomPages([]ManagerCustomPageConfig{}); got == nil || len(got) != 0 {
		t.Fatalf("explicit empty list was not preserved: %#v", got)
	}
	if got := NormalizeManagerCustomPages(nil); got != nil {
		t.Fatalf("nil list became %#v", got)
	}
}

func TestValidateManagerCustomPages(t *testing.T) {
	valid := []ManagerCustomPageConfig{
		{ID: "status-page_1", Title: "状态面板", URL: "https://status.example.test/path?q=ok"},
	}
	if err := ValidateManagerCustomPages(valid); err != nil {
		t.Fatalf("valid custom page rejected: %v", err)
	}

	tests := []struct {
		name  string
		pages []ManagerCustomPageConfig
		match string
	}{
		{name: "missing id", pages: []ManagerCustomPageConfig{{Title: "Status", URL: "https://example.test"}}, match: "invalid id"},
		{name: "invalid id", pages: []ManagerCustomPageConfig{{ID: "bad/id", Title: "Status", URL: "https://example.test"}}, match: "invalid id"},
		{name: "duplicate id is case insensitive", pages: []ManagerCustomPageConfig{{ID: "Status", Title: "One", URL: "https://one.example.test"}, {ID: "status", Title: "Two", URL: "https://two.example.test"}}, match: "duplicate id"},
		{name: "missing title", pages: []ManagerCustomPageConfig{{ID: "status", URL: "https://example.test"}}, match: "title is required"},
		{name: "unsupported scheme", pages: []ManagerCustomPageConfig{{ID: "status", Title: "Status", URL: "javascript:alert(1)"}}, match: "URL is invalid"},
		{name: "embedded credentials", pages: []ManagerCustomPageConfig{{ID: "status", Title: "Status", URL: "https://user:pass@example.test"}}, match: "must not contain credentials"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManagerCustomPages(tt.pages)
			if err == nil || !strings.Contains(err.Error(), tt.match) {
				t.Fatalf("error = %v, want substring %q", err, tt.match)
			}
		})
	}
}
