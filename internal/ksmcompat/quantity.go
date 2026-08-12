package ksmcompat

import (
	"fmt"
	"strconv"
)

// suffixes maps a Kubernetes Quantity suffix to its multiplier. The binary set
// (Ki, Mi, …) and the decimal set (k, M, …) differ by more than 2% at Gi and
// keep diverging, so treating one as the other silently rescales every capacity
// metric derived from it.
//
// "m" is the odd one out: it is a *milli* factor, used for fractional CPU
// ("500m" = half a core), not a multiple.
var suffixes = map[string]float64{
	"":   1,
	"m":  0.001,
	"k":  1e3,
	"M":  1e6,
	"G":  1e9,
	"T":  1e12,
	"P":  1e15,
	"E":  1e18,
	"Ki": 1 << 10,
	"Mi": 1 << 20,
	"Gi": 1 << 30,
	"Ti": 1 << 40,
	"Pi": 1 << 50,
	"Ei": 1 << 60,
}

// parseQuantity converts a Kubernetes Quantity string to a float64.
//
// It returns an error rather than a zero value for anything it cannot read: a
// quantity that silently becomes 0 turns into a metric asserting the node has
// no capacity, which is a worse outcome than the series being absent.
func parseQuantity(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty quantity")
	}

	// 접미사는 뒤에서부터 긴 것(2자)을 먼저 본다 — "Mi" 를 "M" 으로 읽으면
	// 1.048576 배가 아니라 1.0 배가 되어 값이 조용히 어긋난다.
	num, suffix := s, ""
	for _, n := range []int{2, 1} {
		if len(s) <= n {
			continue
		}
		cand := s[len(s)-n:]
		if _, ok := suffixes[cand]; ok {
			// 지수 표기("1e3")의 뒷자리를 접미사로 오인하지 않도록, 남은 앞부분이
			// 숫자로 끝나는지 본다.
			head := s[:len(s)-n]
			if head != "" && isDigitOrDot(head[len(head)-1]) {
				num, suffix = head, cand
				break
			}
		}
	}

	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("parse quantity %q: %w", s, err)
	}
	return v * suffixes[suffix], nil
}

func isDigitOrDot(b byte) bool {
	return (b >= '0' && b <= '9') || b == '.'
}

// resourceUnit reports the unit label kube-state-metrics attaches to a given
// resource in kube_node_status_capacity / _allocatable. Matching it matters:
// the label is part of the series identity, so a different value silently
// creates a parallel series instead of the one existing queries select.
func resourceUnit(resource string) string {
	switch resource {
	case "cpu":
		return "core"
	case "memory", "ephemeral-storage":
		return "byte"
	case "pods":
		return "integer"
	default:
		return "integer"
	}
}
