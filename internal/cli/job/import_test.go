// SPDX-License-Identifier: GPL-3.0-or-later

package job

import "testing"

// A properties CSV may or may not carry a header, and reading a data row as one
// silently drops the first property in the file.
func TestHeaderDetection(t *testing.T) {
	tests := []struct {
		name   string
		record []string
		want   bool
	}{
		{"full header", []string{"name", "type", "example"}, true},
		{"name only", []string{"name"}, true},
		{"reordered", []string{"example", "name"}, true},
		{"mixed case", []string{"Name", "Example"}, true},
		{"data row", []string{"make", "Ford"}, false},
		{"no name column", []string{"type", "example"}, false},
		{"empty", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := headerOf(tt.record) != nil; got != tt.want {
				t.Errorf("headerOf(%v) detected = %v, want %v", tt.record, got, tt.want)
			}
		})
	}
}
