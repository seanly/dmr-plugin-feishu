package main

import (
	"reflect"
	"testing"
)

func TestParseApprovalChoice(t *testing.T) {
	tests := []struct {
		in     string
		want   int32
		wantOK bool
	}{
		{"y", choiceApprovedOnce, true},
		{"n", choiceDenied, true},
		{"x", choiceDenied, true},
		{"", choiceDenied, true},
		{"/prometheus-clusters 查询", choiceDenied, true},
		{"yes", choiceApprovedOnce, true},
	}
	for _, tt := range tests {
		got, ok := parseApprovalChoice(tt.in)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("parseApprovalChoice(%q) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestParseBatchApprovalChoice(t *testing.T) {
	total := 3
	tests := []struct {
		in      string
		wantCh  int32
		wantIdx []int32
		wantOK  bool
	}{
		{"y", choiceApprovedOnce, nil, true},
		{"yes", choiceApprovedOnce, nil, true},
		{"s", choiceApprovedSess, nil, true},
		{"session", choiceApprovedSess, nil, true},
		{"a", choiceApprovedAlways, nil, true},
		{"always", choiceApprovedAlways, nil, true},
		{"n", choiceDenied, nil, true},
		{"no", choiceDenied, nil, true},
		{"", choiceDenied, nil, true},
		{"1", choiceApprovedOnce, []int32{0}, true},
		{"1, 3", choiceApprovedOnce, []int32{0, 2}, true},
		{"2", choiceApprovedOnce, []int32{1}, true},
		{"4", choiceDenied, nil, true}, // invalid index -> deny (CLI)
		{"oops", choiceDenied, nil, true},
		{"/prometheus-clusters 查询 节点监控cpu mem", choiceDenied, nil, true},
	}
	for _, tt := range tests {
		got, ok := parseBatchApprovalChoice(tt.in, total)
		if ok != tt.wantOK {
			t.Fatalf("parseBatchApprovalChoice(%q): ok=%v want %v", tt.in, ok, tt.wantOK)
		}
		if !tt.wantOK {
			continue
		}
		if got.choice != tt.wantCh {
			t.Errorf("parseBatchApprovalChoice(%q): choice=%d want %d", tt.in, got.choice, tt.wantCh)
		}
		if !reflect.DeepEqual(got.indices, tt.wantIdx) {
			t.Errorf("parseBatchApprovalChoice(%q): indices=%v want %v", tt.in, got.indices, tt.wantIdx)
		}
	}
}
