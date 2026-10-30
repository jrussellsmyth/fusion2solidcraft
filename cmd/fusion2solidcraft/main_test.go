package main

import (
	"reflect"
	"testing"
)

func TestParseSkipSteps(t *testing.T) {
	cases := []struct {
		spec string
		want map[int]bool
		err  bool
	}{
		{spec: "", want: map[int]bool{}},
		{spec: "51", want: map[int]bool{51: true}},
		{spec: "51,52", want: map[int]bool{51: true, 52: true}},
		{spec: "40-45", want: map[int]bool{40: true, 41: true, 42: true, 43: true, 44: true, 45: true}},
		{spec: "10, 20-22 ,30", want: map[int]bool{10: true, 20: true, 21: true, 22: true, 30: true}},
		{spec: "5-3", err: true},
		{spec: "abc", err: true},
		{spec: "1,", want: map[int]bool{1: true}},
	}
	for _, tc := range cases {
		got, err := parseSkipSteps(tc.spec)
		if tc.err {
			if err == nil {
				t.Errorf("parseSkipSteps(%q): expected error", tc.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSkipSteps(%q): unexpected error %v", tc.spec, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseSkipSteps(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}
