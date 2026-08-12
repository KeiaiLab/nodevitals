package ksmcompat

import "testing"

// kube_node_status_capacity 는 API 가 돌려주는 Quantity 문자열을 숫자로 옮긴
// 값이다. 접미사를 잘못 읽으면 값이 1024 배씩 어긋나는데, 메트릭 자체는 정상으로
// 보이므로 용량 기반 알림이 조용히 틀린 기준으로 돈다.
func TestParseQuantity(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"7", 7},
		{"0", 0},
		{"500m", 0.5},  // cpu 밀리코어
		{"1500m", 1.5}, // 1 코어를 넘는 밀리코어
		{"12345Ki", 12641280},
		{"64Mi", 67108864},
		{"2Gi", 2147483648},
		{"1Ti", 1099511627776},
		{"1k", 1000}, // 10진 접두사 — Ki 와 다르다
		{"1M", 1000000},
		{"1G", 1000000000},
		{"123456789", 123456789},
		{"1e3", 1000}, // 지수 표기도 유효한 Quantity 다
	}
	for _, c := range cases {
		got, err := parseQuantity(c.in)
		if err != nil {
			t.Errorf("parseQuantity(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseQuantity(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// 읽을 수 없는 값에 0 을 돌려주면 "용량 0" 이라는 거짓 사실이 메트릭이 된다.
func TestParseQuantityRejectsGarbageRatherThanReturningZero(t *testing.T) {
	for _, in := range []string{"", "abc", "12Xi", "1.2.3", "Ki"} {
		if got, err := parseQuantity(in); err == nil {
			t.Errorf("parseQuantity(%q) = %v with no error; an unreadable quantity must not become a number", in, got)
		}
	}
}
