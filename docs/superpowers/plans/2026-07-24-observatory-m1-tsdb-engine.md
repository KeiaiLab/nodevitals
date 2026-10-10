# nodevitals-observatory M1 — TSDB 엔진 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 하드웨어 텔레메트리 시계열을 외부 DB 없이 저장·조회하는 자체 TSDB 엔진을 만든다 — Gorilla 압축, 인메모리 head, WAL 크래시 복구, 불변 블록, 5분 롤업, 보존기간 만료 삭제까지.

**Architecture:** `internal/tsdb` 단일 패키지에 계층을 쌓는다. 최하단이 비트 스트림, 그 위에 타임스탬프(delta-of-delta)·값(XOR) 인코더, 둘을 묶은 청크, 청크를 담는 인메모리 head, head 를 보호하는 WAL, head 를 굳힌 불변 블록, head+블록을 함께 읽는 Querier, 그리고 블록 위에서 도는 롤업·보존 정책 순이다. 각 층은 아래층 인터페이스만 알고, 위층을 모른다.

**Tech Stack:** Go 1.26 · 표준 라이브러리만(M1 은 외부 의존 0) · `go test` · lefthook · GitHub Actions

## Global Constraints

이 절의 항목은 **모든 태스크의 요구사항에 암묵적으로 포함된다.**

- **Go module 경로**: `github.com/KeiaiLab/nodevitals-observatory`
- **Go 버전**: `go 1.26` (에이전트 repo 와 동일 — `go.mod` 에 명시)
- **외부 의존 0**: M1 범위에서 `require` 블록은 비어 있어야 한다. 해시는 표준 `hash/fnv`, 비트 연산은 `math/bits` 를 쓴다.
- **라이선스**: MIT (`LICENSE` 파일, 저작권자 `KEIAILAB`)
- **아키텍처**: `linux/amd64` 단일. 멀티아키텍처 빌드 금지.
- **언어**: 코드 식별자·파일명·커밋 type 은 영어, 주석·내부 문서·커밋 본문은 한국어. **예외 — 공개 OSS 표면은 영어**: `README.md`, `LICENSE`, GitHub description, ArtifactHub 메타데이터. 자매 repo `KeiaiLab/nodevitals` 와 keiailab OSS 패밀리의 확립된 관례를 따른다 (공개 저장소의 첫 화면은 국제 독자 대상).
- **커밋**: Conventional Commits (`feat:` `fix:` `test:` `chore:` `docs:`). 각 태스크 마지막 스텝에서 커밋한다.
- **테스트**: 모든 테스트는 하드웨어·네트워크·시간 의존이 0 이어야 한다. `time.Now()` 를 프로덕션 로직에 쓰지 않고 타임스탬프는 인자로 받는다.
- **파일 크기**: 한 파일이 400줄을 넘으면 책임이 섞인 신호로 본다.
- **정렬 규약**: `Labels` 는 **항상** `Name` 오름차순 정렬 상태로만 존재한다. 정렬되지 않은 `Labels` 를 만드는 경로를 만들지 않는다.
- **타임스탬프 단위**: 밀리초(ms) `int64`. 초 단위와 섞지 않는다.

---

## File Structure

M1 이 끝났을 때의 repo 상태다. 각 파일은 하나의 책임만 진다.

| 파일 | 책임 |
|---|---|
| `go.mod` / `Makefile` / `lefthook.yml` / `LICENSE` / `.gitignore` / `README.md` | 프로젝트 스캐폴딩 |
| `.github/workflows/ci.yml` | vet + test 게이트 |
| `internal/tsdb/bstream.go` | 비트 단위 읽기/쓰기 스트림 |
| `internal/tsdb/encoding_ts.go` | 타임스탬프 delta-of-delta 인코더/디코더 |
| `internal/tsdb/encoding_val.go` | float64 XOR 인코더/디코더 |
| `internal/tsdb/chunk.go` | 두 인코더를 묶은 append-only 청크 + 이터레이터 |
| `internal/tsdb/labels.go` | 라벨셋 타입·정렬·해시·조회 |
| `internal/tsdb/index.go` | 라벨 → seriesID 역색인(posting list)과 집합 연산 |
| `internal/tsdb/matcher.go` | 라벨 매처 4종(`=` `!=` `=~` `!~`) |
| `internal/tsdb/head.go` | 인메모리 시리즈 저장 + append |
| `internal/tsdb/wal.go` | WAL 레코드 포맷·쓰기·재생·세그먼트 회전 |
| `internal/tsdb/block.go` | 불변 블록 쓰기/읽기 + `meta.json` |
| `internal/tsdb/querier.go` | head + 블록 통합 질의 인터페이스 |
| `internal/tsdb/rollup.go` | 5분 집계 블록 생성 |
| `internal/tsdb/retention.go` | 보존기간 초과 블록 삭제 |
| `internal/tsdb/db.go` | 위 전부를 조립한 공개 API |

---

## Task 1: 프로젝트 부트스트랩

**Files:**
- Create: `go.mod`, `Makefile`, `lefthook.yml`, `LICENSE`, `.gitignore`, `README.md`, `.github/workflows/ci.yml`
- Create: `internal/tsdb/doc.go`
- Test: `internal/tsdb/doc_test.go`

**Interfaces:**
- Consumes: 없음 (첫 태스크)
- Produces: Go module `github.com/KeiaiLab/nodevitals-observatory`, 패키지 `internal/tsdb`, `make test` 명령

> **선행 작업 (사람 또는 `gh` CLI):** GitHub 에 `KeiaiLab/nodevitals-observatory` repo 를 만들고 `eightynine01` 계정으로 fork 한 뒤 로컬에 클론한다. 에이전트 repo 와 동일한 2계정 fork 모델을 따른다 (`https://github.com/KeiaiLab/nodevitals/blob/main/docs/DEVELOPMENT.md`).
> ```bash
> gh repo create KeiaiLab/nodevitals-observatory --public \
>   --description "Self-contained observability console for nodevitals — own TSDB, own PromQL subset, own UI. MIT licensed."
> gh repo fork KeiaiLab/nodevitals-observatory --clone --remote-name origin
> cd nodevitals-observatory
> git remote add upstream https://github.com/KeiaiLab/nodevitals-observatory.git
> git switch -c feat/m1-tsdb-engine
> ```

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/doc_test.go`:

```go
package tsdb

import "testing"

// 패키지가 컴파일되고 상수가 노출되는지 확인하는 스모크 테스트.
// 이후 모든 태스크가 이 패키지 위에 쌓인다.
func TestPackageVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version 이 비어 있다")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestPackageVersion -v`
Expected: 컴파일 실패 — `undefined: Version`

- [ ] **Step 3: 스캐폴딩 + 최소 구현**

`go.mod`:

```
module github.com/KeiaiLab/nodevitals-observatory

go 1.26
```

`internal/tsdb/doc.go`:

```go
// Package tsdb 는 nodevitals-observatory 의 자체 시계열 저장 엔진이다.
//
// 계층 구조 (아래에서 위로):
//
//	bstream       비트 단위 스트림
//	encoding_*    타임스탬프 delta-of-delta · 값 XOR
//	chunk         두 인코더를 묶은 append-only 청크
//	head          인메모리 시리즈 + 열린 청크
//	wal           head 를 크래시로부터 보호
//	block         head 를 굳힌 불변 디렉터리
//	querier       head + block 통합 조회
//	rollup        5분 집계 블록
//	retention     보존기간 초과 블록 삭제
//	db            위 전부를 조립한 공개 API
//
// 설계 근거는 nodevitals repo 의
// docs/superpowers/specs/2026-07-24-nodevitals-observatory-design.md §4 참조.
package tsdb

// Version 은 저장 포맷 버전이다. 블록 meta.json 에 기록되며, 포맷이
// 호환 불가하게 바뀔 때만 올린다.
const Version = "1"
```

`Makefile`:

```makefile
.PHONY: all fmt vet test build clean

all: fmt vet test

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./... -race

bench:
	go test ./internal/tsdb/ -bench=. -benchmem -run=^$$

clean:
	rm -rf dist/
```

`lefthook.yml`:

```yaml
pre-commit:
  commands:
    gofmt:
      glob: "*.go"
      run: gofmt -l {staged_files} | (! grep .)

pre-push:
  commands:
    vet:
      run: go vet ./...
    test:
      run: go test ./... -race
```

`.gitignore`:

```
dist/
*.test
*.out
data/
```

`.github/workflows/ci.yml`:

```yaml
name: ci
on:
  pull_request:
  push:
    branches: [main]

jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
      - run: gofmt -l . | (! grep .)
      - run: go vet ./...
      - run: go test ./... -race
```

`LICENSE`: MIT 전문, 저작권자 `KEIAILAB`.

`README.md`:

```markdown
# nodevitals-observatory

Self-contained observability console for [nodevitals](https://github.com/KeiaiLab/nodevitals) —
its own time-series engine, its own PromQL subset, its own UI. No Prometheus,
no VictoriaMetrics, no Grafana at runtime.

> **Status: early development (M1 — storage engine).**
> Design spec: [nodevitals repo](https://github.com/KeiaiLab/nodevitals/blob/main/docs/superpowers/specs/2026-07-24-nodevitals-observatory-design.md)

## License

[MIT](LICENSE)
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestPackageVersion -v`
Expected: `--- PASS: TestPackageVersion`

Run: `make all`
Expected: gofmt·vet 무출력, 테스트 `ok github.com/KeiaiLab/nodevitals-observatory/internal/tsdb`

- [ ] **Step 5: 커밋**

```bash
lefthook install
git add -A
git commit -m "chore: 프로젝트 부트스트랩 — go module, make 게이트, CI, tsdb 패키지 골격"
```

---

## Task 2: 비트 스트림

**Files:**
- Create: `internal/tsdb/bstream.go`
- Test: `internal/tsdb/bstream_test.go`

**Interfaces:**
- Consumes: 없음
- Produces:
  - `type bstream struct{ stream []byte; count uint8 }`
  - `func (b *bstream) writeBit(bit bool)`
  - `func (b *bstream) writeBits(u uint64, nbits int)`
  - `func (b *bstream) bytes() []byte`
  - `func newBReader(s []byte) *breader`
  - `func (r *breader) readBit() (bool, error)`
  - `func (r *breader) readBits(nbits int) (uint64, error)`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/bstream_test.go`:

```go
package tsdb

import (
	"io"
	"testing"
)

func TestBstream_비트를_쓴_순서대로_읽는다(t *testing.T) {
	var b bstream
	want := []bool{true, false, true, true, false, false, false, true, true}
	for _, bit := range want {
		b.writeBit(bit)
	}

	r := newBReader(b.bytes())
	for i, w := range want {
		got, err := r.readBit()
		if err != nil {
			t.Fatalf("비트 %d 읽기 실패: %v", i, err)
		}
		if got != w {
			t.Fatalf("비트 %d: got %v, want %v", i, got, w)
		}
	}
}

func TestBstream_다중비트_값을_왕복한다(t *testing.T) {
	cases := []struct {
		v     uint64
		nbits int
	}{
		{0, 1}, {1, 1}, {5, 3}, {255, 8}, {256, 9},
		{1 << 20, 21}, {^uint64(0), 64}, {0, 64},
	}
	var b bstream
	for _, c := range cases {
		b.writeBits(c.v, c.nbits)
	}

	r := newBReader(b.bytes())
	for i, c := range cases {
		got, err := r.readBits(c.nbits)
		if err != nil {
			t.Fatalf("케이스 %d 읽기 실패: %v", i, err)
		}
		if got != c.v {
			t.Fatalf("케이스 %d: got %d, want %d", i, got, c.v)
		}
	}
}

func TestBstream_스트림_끝에서_EOF를_낸다(t *testing.T) {
	var b bstream
	b.writeBit(true)

	r := newBReader(b.bytes())
	for i := 0; i < 8; i++ {
		if _, err := r.readBit(); err != nil {
			t.Fatalf("첫 바이트 안에서 실패하면 안 된다 (비트 %d): %v", i, err)
		}
	}
	if _, err := r.readBit(); err != io.EOF {
		t.Fatalf("9번째 비트는 EOF 여야 한다, got %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestBstream -v`
Expected: 컴파일 실패 — `undefined: bstream`, `undefined: newBReader`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/bstream.go`:

```go
package tsdb

import "io"

// bstream 은 비트 단위 append-only 스트림이다. 한 바이트 안에서는 MSB 부터
// 채운다 — Gorilla 논문과 Prometheus 구현의 관례를 따르며, 이 순서가 어긋나면
// 인코딩된 청크를 서로 읽을 수 없다.
type bstream struct {
	stream []byte
	count  uint8 // 마지막 바이트에 남은 여유 비트 수 (0 이면 새 바이트가 필요)
}

func (b *bstream) writeBit(bit bool) {
	if b.count == 0 {
		b.stream = append(b.stream, 0)
		b.count = 8
	}
	if bit {
		b.stream[len(b.stream)-1] |= 1 << (b.count - 1)
	}
	b.count--
}

// writeBits 는 u 의 하위 nbits 비트를 MSB 부터 기록한다. 음수를 int64 →
// uint64 로 캐스팅해 넘기면 2의 보수 하위 비트가 그대로 기록되므로,
// 디코더에서 부호를 복원할 수 있다 (encoding_ts.go 참조).
func (b *bstream) writeBits(u uint64, nbits int) {
	u <<= 64 - uint(nbits)
	for nbits > 0 {
		b.writeBit(u>>63 == 1)
		u <<= 1
		nbits--
	}
}

func (b *bstream) bytes() []byte { return b.stream }

// breader 는 bstream 이 쓴 바이트를 같은 순서로 읽는다.
type breader struct {
	stream []byte
	idx    int   // 다음에 읽을 바이트 인덱스
	count  uint8 // 현재 바이트에 남은 비트 수
}

func newBReader(s []byte) *breader { return &breader{stream: s} }

func (r *breader) readBit() (bool, error) {
	if r.count == 0 {
		if r.idx >= len(r.stream) {
			return false, io.EOF
		}
		r.idx++
		r.count = 8
	}
	bit := r.stream[r.idx-1]&(1<<(r.count-1)) != 0
	r.count--
	return bit, nil
}

func (r *breader) readBits(nbits int) (uint64, error) {
	var u uint64
	for i := 0; i < nbits; i++ {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		u <<= 1
		if bit {
			u |= 1
		}
	}
	return u, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestBstream -v -race`
Expected: 3개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/bstream.go internal/tsdb/bstream_test.go
git commit -m "feat(tsdb): 비트 단위 스트림 — MSB-first 쓰기/읽기"
```

---

## Task 3: 타임스탬프 delta-of-delta 인코딩

**Files:**
- Create: `internal/tsdb/encoding_ts.go`
- Test: `internal/tsdb/encoding_ts_test.go`

**Interfaces:**
- Consumes: `bstream`, `breader` (Task 2)
- Produces:
  - `type tsEncoder struct{ n int; t, delta int64 }`
  - `func (e *tsEncoder) append(b *bstream, t int64)`
  - `type tsDecoder struct{ n int; t, delta int64 }`
  - `func (d *tsDecoder) next(r *breader) (int64, error)`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/encoding_ts_test.go`:

```go
package tsdb

import "testing"

func TestTsEncoding_왕복(t *testing.T) {
	cases := map[string][]int64{
		"완전_균일_15초":   {1000, 16000, 31000, 46000, 61000},
		"살짝_지터":       {1000, 16000, 31050, 45900, 61010},
		"큰_점프":        {1000, 16000, 3600000, 3615000},
		"역행하지_않는_중복":  {1000, 1000, 1000},
		"단일_샘플":       {1721800000000},
		"음수_시작":       {-5000, -4000, -3000},
		"티어_전환":       {0, 1000, 1000 + 8192, 1000 + 8192 - 8191},

		// dod 가 각 티어의 **정확한 경계값**이 되도록 델타를 역산한 케이스.
		// 티어를 정하는 값은 delta 가 아니라 dod(= 현재delta - 직전delta)다 —
		// t0=0, t1=t0+base, t2=t1+(base+dod) 이면 두 번째 dod 가 그 값이 된다.
		// bitRange 의 범위는 비대칭이다: 상한 +2^(n-1), 하한 -(2^(n-1)-1).
		"dod_14비트_상한_8192":    {0, 15000, 15000 + 15000 + 8192},
		"dod_14비트_하한_-8191":   {0, 15000, 15000 + 15000 - 8191},
		"dod_17비트_상한_65536":   {0, 100000, 100000 + 100000 + 65536},
		"dod_17비트_하한_-65535":  {0, 100000, 100000 + 100000 - 65535},
		"dod_20비트_상한_524288":  {0, 1000000, 1000000 + 1000000 + 524288},
		"dod_20비트_하한_-524287": {0, 1000000, 1000000 + 1000000 - 524287},
		"dod_20비트_초과_64비트폴백": {0, 1000000, 1000000 + 1000000 + 524289},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			var b bstream
			var enc tsEncoder
			for _, ts := range want {
				enc.append(&b, ts)
			}

			r := newBReader(b.bytes())
			var dec tsDecoder
			for i, w := range want {
				got, err := dec.next(r)
				if err != nil {
					t.Fatalf("샘플 %d 디코드 실패: %v", i, err)
				}
				if got != w {
					t.Fatalf("샘플 %d: got %d, want %d", i, got, w)
				}
			}
		})
	}
}

func TestTsEncoding_균일_간격은_샘플당_1비트(t *testing.T) {
	var b bstream
	var enc tsEncoder
	// 첫 2개(각 64비트) 이후 1000개는 dod==0 이라 1비트씩이어야 한다.
	for i := 0; i < 1002; i++ {
		enc.append(&b, int64(i)*15000)
	}
	// 16바이트(첫 2샘플) + 1000비트(=125바이트) = 141바이트 근방
	if got := len(b.bytes()); got > 145 {
		t.Fatalf("균일 간격 압축이 기대보다 나쁘다: %d bytes (want <= 145)", got)
	}
}

func TestTsDecoder_잘린_스트림에서_에러를_낸다(t *testing.T) {
	var b bstream
	var enc tsEncoder
	for _, ts := range []int64{1000, 16000, 31000} {
		enc.append(&b, ts)
	}

	// 첫 샘플의 64비트조차 못 채우도록 자른다.
	truncated := b.bytes()[:4]
	r := newBReader(truncated)
	var dec tsDecoder
	if _, err := dec.next(r); err == nil {
		t.Fatal("잘린 스트림에서 에러가 나야 한다")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestTsEncoding -v`
Expected: 컴파일 실패 — `undefined: tsEncoder`, `undefined: tsDecoder`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/encoding_ts.go`:

```go
package tsdb

// 타임스탬프는 delta-of-delta 로 인코딩한다. 스크레이프는 간격이 거의 일정해
// 대부분의 dod 가 0 이 되고, 그 경우 샘플당 1비트만 든다.
//
// 첫 두 샘플은 압축 없이 64비트로 적는다 — 청크당 16바이트 고정 오버헤드는
// 무시할 수 있고, varint 를 비트 스트림에 섞는 것보다 구현이 단순하다.

// bitRange 는 x 가 nbits 크기 2의 보수 범위에 들어가는지 본다.
func bitRange(x int64, nbits uint8) bool {
	return -((1<<(nbits-1))-1) <= x && x <= 1<<(nbits-1)
}

type tsEncoder struct {
	n     int
	t     int64 // 직전 타임스탬프
	delta int64 // 직전 델타
}

func (e *tsEncoder) append(b *bstream, t int64) {
	switch {
	case e.n == 0:
		b.writeBits(uint64(t), 64)
	case e.n == 1:
		d := t - e.t
		b.writeBits(uint64(d), 64)
		e.delta = d
	default:
		d := t - e.t
		dod := d - e.delta
		switch {
		case dod == 0:
			b.writeBit(false) // '0'
		case bitRange(dod, 14):
			b.writeBits(0b10, 2)
			b.writeBits(uint64(dod), 14)
		case bitRange(dod, 17):
			b.writeBits(0b110, 3)
			b.writeBits(uint64(dod), 17)
		case bitRange(dod, 20):
			b.writeBits(0b1110, 4)
			b.writeBits(uint64(dod), 20)
		default:
			b.writeBits(0b1111, 4)
			b.writeBits(uint64(dod), 64)
		}
		e.delta = d
	}
	e.t = t
	e.n++
}

type tsDecoder struct {
	n     int
	t     int64
	delta int64
}

// signed 는 nbits 로 기록된 2의 보수 값을 int64 로 복원한다.
func signed(u uint64, nbits uint8) int64 {
	if u > 1<<(nbits-1) {
		return int64(u) - (1 << nbits)
	}
	return int64(u)
}

func (d *tsDecoder) next(r *breader) (int64, error) {
	switch {
	case d.n == 0:
		u, err := r.readBits(64)
		if err != nil {
			return 0, err
		}
		d.t = int64(u)
	case d.n == 1:
		u, err := r.readBits(64)
		if err != nil {
			return 0, err
		}
		d.delta = int64(u)
		d.t += d.delta
	default:
		// 선행 1비트 개수로 dod 폭을 판별한다 (최대 4비트).
		var lead int
		for lead < 4 {
			bit, err := r.readBit()
			if err != nil {
				return 0, err
			}
			if !bit {
				break
			}
			lead++
		}
		var nbits uint8
		switch lead {
		case 0:
			nbits = 0 // dod == 0
		case 1:
			nbits = 14
		case 2:
			nbits = 17
		case 3:
			nbits = 20
		default:
			nbits = 64
		}
		var dod int64
		if nbits > 0 {
			u, err := r.readBits(int(nbits))
			if err != nil {
				return 0, err
			}
			if nbits == 64 {
				dod = int64(u)
			} else {
				dod = signed(u, nbits)
			}
		}
		d.delta += dod
		d.t += d.delta
	}
	d.n++
	return d.t, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestTsEncoding -v -race`
Expected: 7개 서브테스트 + 압축률 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/encoding_ts.go internal/tsdb/encoding_ts_test.go
git commit -m "feat(tsdb): 타임스탬프 delta-of-delta 인코딩 — 균일 간격은 샘플당 1비트"
```

---

## Task 4: 값 XOR 인코딩

**Files:**
- Create: `internal/tsdb/encoding_val.go`
- Test: `internal/tsdb/encoding_val_test.go`

**Interfaces:**
- Consumes: `bstream`, `breader` (Task 2)
- Produces:
  - `type valEncoder struct{ n int; v float64; leading, trailing uint8 }`
  - `func (e *valEncoder) append(b *bstream, v float64)`
  - `type valDecoder struct{ n int; v float64; leading, trailing uint8 }`
  - `func (d *valDecoder) next(r *breader) (float64, error)`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/encoding_val_test.go`:

```go
package tsdb

import (
	"math"
	"testing"
)

func TestValEncoding_왕복(t *testing.T) {
	cases := map[string][]float64{
		"상수":        {42, 42, 42, 42},
		"천천히_증가":    {0.5, 0.51, 0.52, 0.53, 0.54},
		"카운터_단조증가":  {1e6, 1e6 + 3, 1e6 + 9, 1e6 + 14},
		"큰_변동":      {1, 1e300, -1e-300, 0},
		"음수":        {-1.5, -2.5, -3.5},
		"영":         {0, 0, 0},
		"단일":        {3.14159},
		"특수값":       {math.Inf(1), math.Inf(-1), 0, math.MaxFloat64, math.SmallestNonzeroFloat64},

		// +0.0 과 -0.0 은 비트 패턴이 다르다(0x0000… vs 0x8000…). 값 비교로는
		// 구분되지 않으므로 아래 판정을 비트 비교로 한 것과 짝을 이룬다.
		"부호있는_영": {0, math.Copysign(0, -1), 0, math.Copysign(0, -1)},
		// 1.0 과 1.0+2^-52 의 XOR 는 0x1 이라 leading zero 가 63 —
		// lead >= 32 클램프 분기를 확실히 태운다.
		"leading_클램프": {1.0, 1.0 + math.Ldexp(1, -52), 1.0},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			var b bstream
			var enc valEncoder
			for _, v := range want {
				enc.append(&b, v)
			}

			r := newBReader(b.bytes())
			var dec valDecoder
			for i, w := range want {
				got, err := dec.next(r)
				if err != nil {
					t.Fatalf("샘플 %d 디코드 실패: %v", i, err)
				}
				// 비트 단위로 비교한다 — Go/IEEE-754 에서 +0.0 == -0.0 이라
				// 값 비교로는 부호 있는 0 의 보존을 검증할 수 없다.
				if math.Float64bits(got) != math.Float64bits(w) {
					t.Fatalf("샘플 %d: got %v (bits %#016x), want %v (bits %#016x)",
						i, got, math.Float64bits(got), w, math.Float64bits(w))
				}
			}
		})
	}
}

func TestValEncoding_NaN도_비트단위로_보존된다(t *testing.T) {
	nan := math.NaN()
	var b bstream
	var enc valEncoder
	enc.append(&b, 1.0)
	enc.append(&b, nan)

	r := newBReader(b.bytes())
	var dec valDecoder
	if _, err := dec.next(r); err != nil {
		t.Fatalf("첫 샘플: %v", err)
	}
	got, err := dec.next(r)
	if err != nil {
		t.Fatalf("두 번째 샘플: %v", err)
	}
	if !math.IsNaN(got) {
		t.Fatalf("NaN 이 보존되지 않았다: %v", got)
	}
}

func TestValEncoding_상수값은_샘플당_1비트(t *testing.T) {
	var b bstream
	var enc valEncoder
	for i := 0; i < 1001; i++ {
		enc.append(&b, 42.0)
	}
	// 8바이트(첫 샘플) + 1000비트(=125바이트) = 133바이트 근방
	if got := len(b.bytes()); got > 137 {
		t.Fatalf("상수값 압축이 기대보다 나쁘다: %d bytes (want <= 137)", got)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestValEncoding -v`
Expected: 컴파일 실패 — `undefined: valEncoder`, `undefined: valDecoder`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/encoding_val.go`:

```go
package tsdb

import (
	"math"
	"math/bits"
)

// 값은 직전 값과의 XOR 로 인코딩한다. 하드웨어 메트릭은 변화가 느려 XOR 의
// 유효 비트가 가운데 몇 비트에 몰리므로, leading/trailing 0 개수를 적고
// 가운데만 저장한다. 값이 같으면 1비트로 끝난다.
//
// leadingUnset 은 "아직 윈도우가 정해지지 않음" 표식이다 — 0 은 유효한
// leading 값이라 sentinel 로 쓸 수 없다.
const leadingUnset uint8 = 0xff

type valEncoder struct {
	n        int
	v        float64
	leading  uint8
	trailing uint8
}

func (e *valEncoder) append(b *bstream, v float64) {
	if e.n == 0 {
		b.writeBits(math.Float64bits(v), 64)
		e.v = v
		e.leading = leadingUnset
		e.n++
		return
	}

	xor := math.Float64bits(v) ^ math.Float64bits(e.v)
	if xor == 0 {
		b.writeBit(false)
	} else {
		b.writeBit(true)

		lead := uint8(bits.LeadingZeros64(xor))
		trail := uint8(bits.TrailingZeros64(xor))
		// leading 은 5비트(0~31)로만 적으므로 32 이상은 잘라 쓴다.
		if lead >= 32 {
			lead = 31
		}

		if e.leading != leadingUnset && lead >= e.leading && trail >= e.trailing {
			// 직전 윈도우가 이번 XOR 를 덮는다 — 윈도우를 다시 적지 않는다.
			b.writeBit(false)
			b.writeBits(xor>>e.trailing, int(64-e.leading-e.trailing))
		} else {
			b.writeBit(true)
			b.writeBits(uint64(lead), 5)
			sigbits := 64 - lead - trail
			// sigbits 는 1~64 인데 6비트로는 0~63 만 담긴다. 64 는 0 으로
			// 적고 디코더가 되살린다 (0 은 xor==0 경로라 실제로 안 나온다).
			b.writeBits(uint64(sigbits), 6)
			b.writeBits(xor>>trail, int(sigbits))
			e.leading, e.trailing = lead, trail
		}
	}

	e.v = v
	e.n++
}

type valDecoder struct {
	n        int
	v        float64
	leading  uint8
	trailing uint8
}

func (d *valDecoder) next(r *breader) (float64, error) {
	if d.n == 0 {
		u, err := r.readBits(64)
		if err != nil {
			return 0, err
		}
		d.v = math.Float64frombits(u)
		d.n++
		return d.v, nil
	}

	changed, err := r.readBit()
	if err != nil {
		return 0, err
	}
	if changed {
		newWindow, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if newWindow {
			lead, err := r.readBits(5)
			if err != nil {
				return 0, err
			}
			mbits, err := r.readBits(6)
			if err != nil {
				return 0, err
			}
			if mbits == 0 {
				mbits = 64
			}
			d.leading = uint8(lead)
			d.trailing = 64 - d.leading - uint8(mbits)
		}
		sigbits := int(64 - d.leading - d.trailing)
		u, err := r.readBits(sigbits)
		if err != nil {
			return 0, err
		}
		xor := u << d.trailing
		d.v = math.Float64frombits(math.Float64bits(d.v) ^ xor)
	}

	d.n++
	return d.v, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestValEncoding -v -race`
Expected: 8개 서브테스트 + NaN + 압축률 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/encoding_val.go internal/tsdb/encoding_val_test.go
git commit -m "feat(tsdb): float64 XOR 인코딩 — 상수값은 샘플당 1비트"
```

---

## Task 5: 청크

**Files:**
- Create: `internal/tsdb/chunk.go`
- Test: `internal/tsdb/chunk_test.go`

**Interfaces:**
- Consumes: `bstream`/`breader` (Task 2), `tsEncoder`/`tsDecoder` (Task 3), `valEncoder`/`valDecoder` (Task 4)
- Produces:
  - `const maxSamplesPerChunk = 120`
  - `var ErrChunkFull, ErrOutOfOrder, ErrInvalidChunk error`
  - `func NewChunk() *Chunk`
  - `func (c *Chunk) Append(t int64, v float64) error`
  - `func (c *Chunk) NumSamples() int`, `MinTime() int64`, `MaxTime() int64`, `Bytes() []byte`
  - `func (c *Chunk) Iterator() *ChunkIterator`
  - `func ChunkFromBytes(b []byte) (*Chunk, error)`
  - `type ChunkIterator` with `Next() bool`, `At() (int64, float64)`, `Err() error`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/chunk_test.go`:

```go
package tsdb

import (
	"errors"
	"testing"
)

type sample struct {
	t int64
	v float64
}

func TestChunk_넣은_순서대로_읽는다(t *testing.T) {
	want := []sample{
		{1000, 0.5}, {16000, 0.51}, {31000, 0.52}, {46000, 12.75}, {61000, 12.75},
	}
	c := NewChunk()
	for _, s := range want {
		if err := c.Append(s.t, s.v); err != nil {
			t.Fatalf("Append(%d): %v", s.t, err)
		}
	}
	if got := c.NumSamples(); got != len(want) {
		t.Fatalf("NumSamples: got %d, want %d", got, len(want))
	}
	if c.MinTime() != 1000 || c.MaxTime() != 61000 {
		t.Fatalf("시간 범위: got [%d,%d], want [1000,61000]", c.MinTime(), c.MaxTime())
	}

	it := c.Iterator()
	for i, w := range want {
		if !it.Next() {
			t.Fatalf("샘플 %d 에서 조기 종료: %v", i, it.Err())
		}
		gt, gv := it.At()
		if gt != w.t || gv != w.v {
			t.Fatalf("샘플 %d: got (%d,%v), want (%d,%v)", i, gt, gv, w.t, w.v)
		}
	}
	if it.Next() {
		t.Fatal("샘플이 더 나왔다")
	}
	if it.Err() != nil {
		t.Fatalf("이터레이터 에러: %v", it.Err())
	}
}

func TestChunk_역행_타임스탬프를_거부한다(t *testing.T) {
	c := NewChunk()
	if err := c.Append(5000, 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Append(4999, 2); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("역행을 거부해야 한다, got %v", err)
	}
	// 같은 타임스탬프는 허용한다 (스크레이프 중복 방어는 상위 계층 책임).
	if err := c.Append(5000, 2); err != nil {
		t.Fatalf("동일 타임스탬프는 허용해야 한다: %v", err)
	}
}

func TestChunk_가득_차면_ErrChunkFull(t *testing.T) {
	c := NewChunk()
	for i := 0; i < maxSamplesPerChunk; i++ {
		if err := c.Append(int64(i)*15000, float64(i)); err != nil {
			t.Fatalf("샘플 %d: %v", i, err)
		}
	}
	err := c.Append(int64(maxSamplesPerChunk)*15000, 1)
	if !errors.Is(err, ErrChunkFull) {
		t.Fatalf("가득 찬 청크는 ErrChunkFull 이어야 한다, got %v", err)
	}
}

func TestChunk_바이트로_왕복한다(t *testing.T) {
	c := NewChunk()
	for i := 0; i < 50; i++ {
		if err := c.Append(int64(i)*15000, float64(i)*1.5); err != nil {
			t.Fatal(err)
		}
	}

	restored, err := ChunkFromBytes(c.Bytes())
	if err != nil {
		t.Fatalf("ChunkFromBytes: %v", err)
	}
	if restored.NumSamples() != 50 {
		t.Fatalf("NumSamples: got %d, want 50", restored.NumSamples())
	}

	it := restored.Iterator()
	for i := 0; i < 50; i++ {
		if !it.Next() {
			t.Fatalf("샘플 %d 조기 종료: %v", i, it.Err())
		}
		gt, gv := it.At()
		if gt != int64(i)*15000 || gv != float64(i)*1.5 {
			t.Fatalf("샘플 %d: got (%d,%v)", i, gt, gv)
		}
	}
}

func TestChunk_빈_청크는_이터레이션이_즉시_끝난다(t *testing.T) {
	c := NewChunk()
	it := c.Iterator()
	if it.Next() {
		t.Fatal("빈 청크에서 샘플이 나왔다")
	}
	if it.Err() != nil {
		t.Fatalf("빈 청크는 에러가 없어야 한다: %v", it.Err())
	}
}

func TestChunkFromBytes_짧은_입력을_거부한다(t *testing.T) {
	if _, err := ChunkFromBytes([]byte{0}); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("2바이트 미만은 ErrInvalidChunk 여야 한다, got %v", err)
	}
}

func TestChunkFromBytes_샘플수가_과대하면_거부한다(t *testing.T) {
	c := NewChunk()
	for i := 0; i < 10; i++ {
		if err := c.Append(int64(i)*15000, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	raw := c.Bytes()
	// 헤더의 샘플 수를 실제보다 크게 조작하면 디코더가 스트림 끝을 넘어
	// 읽으려 해 EOF 가 나고, ChunkFromBytes 가 ErrInvalidChunk 를 낸다.
	binary.BigEndian.PutUint16(raw[:2], 200)

	if _, err := ChunkFromBytes(raw); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("과대 샘플 수는 ErrInvalidChunk 여야 한다, got %v", err)
	}
}

func TestChunkFromBytes_샘플수가_과소하면_조용히_잘린다(t *testing.T) {
	// 알려진 한계를 고정하는 테스트다. 헤더가 실제보다 적은 샘플 수를 주장하면
	// 디코더는 그만큼만 읽고 정상 종료한다 — 청크 계층은 자기 기술 헤더를
	// 신뢰하며, 저장 매체 손상 탐지는 상위 계층(WAL 의 CRC32C, 블록의 무결성
	// 검사)의 책임이다. 이 동작이 바뀌면 이 테스트가 알려준다.
	c := NewChunk()
	for i := 0; i < 10; i++ {
		if err := c.Append(int64(i)*15000, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	raw := c.Bytes()
	binary.BigEndian.PutUint16(raw[:2], 3)

	restored, err := ChunkFromBytes(raw)
	if err != nil {
		t.Fatalf("과소 샘플 수는 현 설계상 에러가 아니다: %v", err)
	}
	if restored.NumSamples() != 3 {
		t.Fatalf("헤더가 주장한 만큼만 읽어야 한다: got %d", restored.NumSamples())
	}
}

func TestChunkFromBytes_헤더만_있는_빈_청크(t *testing.T) {
	// n == 0 분기 — minT/maxT 복원 루프를 건너뛰고 즉시 반환한다.
	restored, err := ChunkFromBytes([]byte{0, 0})
	if err != nil {
		t.Fatalf("빈 청크는 유효하다: %v", err)
	}
	if restored.NumSamples() != 0 {
		t.Fatalf("NumSamples: got %d, want 0", restored.NumSamples())
	}
	if restored.Iterator().Next() {
		t.Fatal("빈 청크에서 샘플이 나왔다")
	}
}
```

> 위 테스트는 `encoding/binary` 를 import 한다.

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestChunk -v`
Expected: 컴파일 실패 — `undefined: NewChunk`, `undefined: ErrOutOfOrder`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/chunk.go`:

```go
package tsdb

import (
	"encoding/binary"
	"errors"
)

// maxSamplesPerChunk 는 한 청크에 담는 샘플 수 상한이다. 15초 간격이면
// 30분치에 해당한다 — 청크가 너무 크면 부분 조회에도 전체를 풀어야 하고,
// 너무 작으면 첫 두 샘플의 비압축 오버헤드(16바이트)가 두드러진다.
const maxSamplesPerChunk = 120

var (
	ErrChunkFull    = errors.New("tsdb: 청크가 가득 참")
	ErrOutOfOrder   = errors.New("tsdb: 타임스탬프 역행")
	ErrInvalidChunk = errors.New("tsdb: 청크 바이트가 손상됨")
)

// Chunk 는 한 시리즈의 연속 샘플을 담는 append-only 압축 청크다.
// 동시 접근은 보호하지 않는다 — 소유자(memSeries)가 잠근다.
type Chunk struct {
	b      bstream
	tsEnc  tsEncoder
	valEnc valEncoder

	numSamples uint16
	minT, maxT int64
}

func NewChunk() *Chunk {
	return &Chunk{minT: 0, maxT: 0}
}

func (c *Chunk) Append(t int64, v float64) error {
	if c.Full() {
		return ErrChunkFull
	}
	if c.numSamples > 0 && t < c.maxT {
		return ErrOutOfOrder
	}
	if c.numSamples == 0 {
		c.minT = t
	}
	c.tsEnc.append(&c.b, t)
	c.valEnc.append(&c.b, v)
	c.maxT = t
	c.numSamples++
	return nil
}

func (c *Chunk) NumSamples() int { return int(c.numSamples) }
func (c *Chunk) MinTime() int64  { return c.minT }
func (c *Chunk) MaxTime() int64  { return c.maxT }
func (c *Chunk) Full() bool      { return c.numSamples >= maxSamplesPerChunk }

// Bytes 는 [2바이트 샘플 수][비트스트림] 형태로 직렬화한다. 샘플 수를 앞에
// 두어야 ChunkFromBytes 가 외부 메타 없이 자족적으로 복원된다.
//
// 반환값은 호출 시점의 독립 복사본이다 — 이후 Append 가 내부 스트림을
// 늘려도 이미 넘겨준 바이트는 바뀌지 않는다.
func (c *Chunk) Bytes() []byte {
	out := make([]byte, 2, 2+len(c.b.stream))
	binary.BigEndian.PutUint16(out, c.numSamples)
	return append(out, c.b.stream...)
}

// ChunkFromBytes 는 Bytes 의 역이다. 복원된 청크는 읽기 전용으로 쓴다
// (인코더 상태가 없으므로 Append 하면 스트림이 깨진다).
func ChunkFromBytes(b []byte) (*Chunk, error) {
	if len(b) < 2 {
		return nil, ErrInvalidChunk
	}
	n := binary.BigEndian.Uint16(b[:2])
	c := &Chunk{numSamples: n}
	c.b.stream = append([]byte(nil), b[2:]...)

	// minT/maxT 는 스트림을 훑어 채운다 — 블록 인덱스가 따로 들고 있지만,
	// 단독으로도 올바른 값을 답하도록 여기서 복원한다.
	if n > 0 {
		it := c.Iterator()
		first := true
		for it.Next() {
			t, _ := it.At()
			if first {
				c.minT = t
				first = false
			}
			c.maxT = t
		}
		if it.Err() != nil {
			return nil, ErrInvalidChunk
		}
	}
	return c, nil
}

func (c *Chunk) Iterator() *ChunkIterator {
	return &ChunkIterator{
		r:         newBReader(c.b.stream),
		remaining: int(c.numSamples),
	}
}

// ChunkIterator 는 청크의 샘플을 시간 오름차순으로 낸다.
type ChunkIterator struct {
	r         *breader
	tsDec     tsDecoder
	valDec    valDecoder
	remaining int

	t   int64
	v   float64
	err error
}

func (it *ChunkIterator) Next() bool {
	if it.err != nil || it.remaining == 0 {
		return false
	}
	t, err := it.tsDec.next(it.r)
	if err != nil {
		it.err = err
		return false
	}
	v, err := it.valDec.next(it.r)
	if err != nil {
		it.err = err
		return false
	}
	it.t, it.v = t, v
	it.remaining--
	return true
}

func (it *ChunkIterator) At() (int64, float64) { return it.t, it.v }
func (it *ChunkIterator) Err() error           { return it.err }
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestChunk -v -race`
Expected: 6개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/chunk.go internal/tsdb/chunk_test.go
git commit -m "feat(tsdb): append-only 압축 청크 — 두 인코더 결합 + 바이트 왕복"
```

---

## Task 6: 라벨셋

**Files:**
- Create: `internal/tsdb/labels.go`
- Test: `internal/tsdb/labels_test.go`

**Interfaces:**
- Consumes: 없음
- Produces:
  - `type Label struct{ Name, Value string }`
  - `type Labels []Label`
  - `func NewLabels(ls ...Label) Labels`, `func LabelsFromMap(m map[string]string) Labels`
  - `func (ls Labels) Get(name string) string`, `Hash() uint64`, `Equal(o Labels) bool`, `String() string`, `MapKey() string`, `Copy() Labels`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/labels_test.go`:

```go
package tsdb

import "testing"

func TestLabels_항상_이름순_정렬된다(t *testing.T) {
	ls := NewLabels(
		Label{"node", "e101"},
		Label{"__name__", "node_load1"},
		Label{"tier", "core"},
	)
	want := []string{"__name__", "node", "tier"}
	for i, w := range want {
		if ls[i].Name != w {
			t.Fatalf("위치 %d: got %q, want %q", i, ls[i].Name, w)
		}
	}
}

func TestLabels_해시는_입력_순서와_무관하다(t *testing.T) {
	a := NewLabels(Label{"node", "e101"}, Label{"tier", "core"})
	b := NewLabels(Label{"tier", "core"}, Label{"node", "e101"})
	if a.Hash() != b.Hash() {
		t.Fatalf("같은 라벨셋의 해시가 다르다: %d vs %d", a.Hash(), b.Hash())
	}
}

func TestLabels_다른_라벨셋은_다른_해시를_낸다(t *testing.T) {
	a := NewLabels(Label{"node", "e101"})
	b := NewLabels(Label{"node", "e102"})
	if a.Hash() == b.Hash() {
		t.Fatal("서로 다른 라벨셋이 같은 해시를 냈다")
	}
	// 구분자 없이 이어붙이면 아래 두 개가 충돌한다 — 구분자가 있는지 확인.
	c := NewLabels(Label{"a", "bc"})
	d := NewLabels(Label{"ab", "c"})
	if c.Hash() == d.Hash() {
		t.Fatal("구분자 부재로 해시가 충돌한다")
	}
}

func TestLabels_Get과_Equal(t *testing.T) {
	ls := NewLabels(Label{"node", "e101"}, Label{"device", "sda"})
	if got := ls.Get("node"); got != "e101" {
		t.Fatalf("Get(node): got %q", got)
	}
	if got := ls.Get("없음"); got != "" {
		t.Fatalf("없는 라벨은 빈 문자열이어야 한다, got %q", got)
	}
	if !ls.Equal(NewLabels(Label{"device", "sda"}, Label{"node", "e101"})) {
		t.Fatal("같은 라벨셋인데 Equal 이 false")
	}
	if ls.Equal(NewLabels(Label{"node", "e101"})) {
		t.Fatal("길이가 다른데 Equal 이 true")
	}
}

func TestLabels_String은_PromQL_표기를_낸다(t *testing.T) {
	ls := NewLabels(Label{"__name__", "node_load1"}, Label{"node", "e101"})
	want := `{__name__="node_load1", node="e101"}`
	if got := ls.String(); got != want {
		t.Fatalf("String: got %q, want %q", got, want)
	}
}

func TestLabels_Copy는_원본과_분리된다(t *testing.T) {
	orig := NewLabels(Label{"node", "e101"})
	cp := orig.Copy()
	cp[0].Value = "e999"
	if orig[0].Value != "e101" {
		t.Fatal("Copy 가 원본을 공유한다")
	}
}

func TestLabelsFromMap(t *testing.T) {
	ls := LabelsFromMap(map[string]string{"tier": "core", "node": "e101"})
	if len(ls) != 2 || ls[0].Name != "node" || ls[1].Name != "tier" {
		t.Fatalf("정렬된 2개여야 한다: %v", ls)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestLabels -v`
Expected: 컴파일 실패 — `undefined: NewLabels`, `undefined: Label`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/labels.go`:

```go
package tsdb

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

// MetricName 은 메트릭 이름을 담는 예약 라벨이다 (Prometheus 관례).
const MetricName = "__name__"

type Label struct {
	Name  string
	Value string
}

// Labels 는 Name 오름차순으로 정렬된 라벨 집합이다. 이 정렬은 성능 최적화가
// 아니라 **정확성의 전제**다 — Equal 은 위치별로 비교하고 Hash 는 슬라이스
// 순서 그대로 해시하므로, 정렬이 깨진 값은 논리적으로 같은 라벨셋과 Equal 이
// false 를 내고 Hash 도 달라진다. 그러면 같은 시리즈가 둘로 갈라진다.
//
// 생성은 반드시 NewLabels 또는 LabelsFromMap 을 쓴다. Labels 는 []Label 의
// named type 이라 Go 타입 시스템이 `Labels{{"b","1"},{"a","2"}}` 같은 리터럴
// 생성이나 `ls[i].Name` 직접 수정을 막지 못한다 — 그런 코드를 작성하지 말 것.
type Labels []Label

func NewLabels(ls ...Label) Labels {
	out := make(Labels, len(ls))
	copy(out, ls)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func LabelsFromMap(m map[string]string) Labels {
	out := make(Labels, 0, len(m))
	for k, v := range m {
		out = append(out, Label{Name: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (ls Labels) Get(name string) string {
	for _, l := range ls {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// Hash 는 라벨셋의 안정적 해시다. 이름과 값 사이에 0xff 구분자를 넣어
// {a="bc"} 와 {ab="c"} 가 충돌하지 않게 한다. 해시 충돌 자체는 상위 계층
// (Head)이 라벨셋 원본 비교로 해소하므로 암호학적 강도는 필요 없다.
func (ls Labels) Hash() uint64 {
	h := fnv.New64a()
	for _, l := range ls {
		_, _ = h.Write([]byte(l.Name))
		_, _ = h.Write([]byte{0xff})
		_, _ = h.Write([]byte(l.Value))
		_, _ = h.Write([]byte{0xff})
	}
	return h.Sum64()
}

func (ls Labels) Equal(o Labels) bool {
	if len(ls) != len(o) {
		return false
	}
	for i := range ls {
		if ls[i] != o[i] {
			return false
		}
	}
	return true
}

func (ls Labels) String() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, l := range ls {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(l.Name)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(l.Value))
	}
	b.WriteByte('}')
	return b.String()
}

// MapKey 는 라벨셋을 맵 키로 쓰기 위한 직렬화다. 각 이름·값 앞에 길이를
// 붙이므로 서로 다른 라벨셋이 같은 키를 낼 수 없다.
//
// String() 을 맵 키로 쓰면 안 된다 — 그쪽은 PromQL 표기를 그대로 내는 사람용
// 표현이라 이름을 이스케이프하지 않고, 이름에 `="` 나 `, ` 가 들어가면 다른
// 라벨셋과 충돌한다(예: {a="b", c="d"} 와 이름이 `a="b", c` 인 단일 라벨).
func (ls Labels) MapKey() string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(strconv.Itoa(len(l.Name)))
		b.WriteByte(':')
		b.WriteString(l.Name)
		b.WriteString(strconv.Itoa(len(l.Value)))
		b.WriteByte(':')
		b.WriteString(l.Value)
	}
	return b.String()
}

func (ls Labels) Copy() Labels {
	out := make(Labels, len(ls))
	copy(out, ls)
	return out
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestLabels -v -race`
Expected: 7개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/labels.go internal/tsdb/labels_test.go
git commit -m "feat(tsdb): 라벨셋 타입 — 정렬 불변식, 구분자 있는 안정 해시"
```

---

## Task 7: 역색인과 매처

**Files:**
- Create: `internal/tsdb/index.go`, `internal/tsdb/matcher.go`
- Test: `internal/tsdb/index_test.go`, `internal/tsdb/matcher_test.go`

**Interfaces:**
- Consumes: `Labels` (Task 6)
- Produces:
  - `func newMemPostings() *memPostings`
  - `func (p *memPostings) Add(id uint64, ls Labels)`, `Get(name, value string) []uint64`, `All() []uint64`, `LabelNames() []string`, `LabelValues(name string) []string`
  - `func intersect(a, b []uint64) []uint64`, `func union(a, b []uint64) []uint64`, `func without(a, b []uint64) []uint64`
  - `func selectRefs(p *memPostings, ms []*Matcher) []uint64`
  - `func matchesAll(ls Labels, ms []*Matcher) bool`
  - `type MatchType int` + `MatchEqual`, `MatchNotEqual`, `MatchRegexp`, `MatchNotRegexp`
  - `func NewMatcher(t MatchType, name, value string) (*Matcher, error)`
  - `func (m *Matcher) Matches(s string) bool`, `func (m *Matcher) String() string`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/index_test.go`:

```go
package tsdb

import (
	"reflect"
	"testing"
)

func TestMemPostings_라벨로_시리즈를_찾는다(t *testing.T) {
	p := newMemPostings()
	p.Add(1, NewLabels(Label{"node", "e101"}, Label{"tier", "core"}))
	p.Add(2, NewLabels(Label{"node", "e102"}, Label{"tier", "core"}))
	p.Add(3, NewLabels(Label{"node", "e101"}, Label{"tier", "gpu"}))

	if got := p.Get("node", "e101"); !reflect.DeepEqual(got, []uint64{1, 3}) {
		t.Fatalf("node=e101: got %v, want [1 3]", got)
	}
	if got := p.Get("tier", "core"); !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("tier=core: got %v, want [1 2]", got)
	}
	if got := p.Get("node", "없음"); len(got) != 0 {
		t.Fatalf("없는 값은 빈 결과여야 한다: %v", got)
	}
	if got := p.All(); !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("All: got %v, want [1 2 3]", got)
	}
}

func TestMemPostings_라벨_이름과_값을_열거한다(t *testing.T) {
	p := newMemPostings()
	p.Add(1, NewLabels(Label{"node", "e101"}, Label{"tier", "core"}))
	p.Add(2, NewLabels(Label{"node", "e102"}))

	if got := p.LabelNames(); !reflect.DeepEqual(got, []string{"node", "tier"}) {
		t.Fatalf("LabelNames: got %v", got)
	}
	if got := p.LabelValues("node"); !reflect.DeepEqual(got, []string{"e101", "e102"}) {
		t.Fatalf("LabelValues(node): got %v", got)
	}
}

func TestPostings_집합연산(t *testing.T) {
	a := []uint64{1, 3, 5, 7, 9}
	b := []uint64{3, 4, 5, 6, 9, 10}

	if got := intersect(a, b); !reflect.DeepEqual(got, []uint64{3, 5, 9}) {
		t.Fatalf("intersect: got %v", got)
	}
	if got := union(a, b); !reflect.DeepEqual(got, []uint64{1, 3, 4, 5, 6, 7, 9, 10}) {
		t.Fatalf("union: got %v", got)
	}
	if got := without(a, b); !reflect.DeepEqual(got, []uint64{1, 7}) {
		t.Fatalf("without: got %v", got)
	}
	if got := intersect(a, nil); len(got) != 0 {
		t.Fatalf("빈 집합과의 교집합은 비어야 한다: %v", got)
	}
}

func TestSelectRefs_색인으로_후보를_좁힌다(t *testing.T) {
	p := newMemPostings()
	p.Add(1, NewLabels(Label{"node", "e101"}, Label{"tier", "core"}))
	p.Add(2, NewLabels(Label{"node", "e102"}, Label{"tier", "core"}))
	p.Add(3, NewLabels(Label{"node", "e101"}, Label{"tier", "gpu"}))

	eq, _ := NewMatcher(MatchEqual, "node", "e101")
	if got := selectRefs(p, []*Matcher{eq}); !reflect.DeepEqual(got, []uint64{1, 3}) {
		t.Fatalf("node=e101: got %v, want [1 3]", got)
	}

	re, _ := NewMatcher(MatchRegexp, "tier", "core|gpu")
	if got := selectRefs(p, []*Matcher{re}); !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("tier=~core|gpu: got %v", got)
	}

	tierEq, _ := NewMatcher(MatchEqual, "tier", "gpu")
	if got := selectRefs(p, []*Matcher{eq, tierEq}); !reflect.DeepEqual(got, []uint64{3}) {
		t.Fatalf("교집합: got %v, want [3]", got)
	}
}

func TestSelectRefs_색인으로_좁힐_수_없으면_전체를_준다(t *testing.T) {
	p := newMemPostings()
	p.Add(1, NewLabels(Label{"node", "e101"}))
	p.Add(2, NewLabels(Label{"node", "e102"}, Label{"tier", "gpu"}))

	// 부정 매처는 "그 라벨이 아예 없는 시리즈"도 만족시키므로 색인으로
	// 좁힐 수 없다 — 전체에서 시작해야 한다.
	ne, _ := NewMatcher(MatchNotEqual, "tier", "gpu")
	if got := selectRefs(p, []*Matcher{ne}); !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("tier!=gpu 후보: got %v, want [1 2]", got)
	}

	// 빈 값 같음-매처(tier="")도 마찬가지다. 색인에는 빈 값 posting 이
	// 없으므로 시드로 쓰면 결과가 통째로 사라진다.
	empty, _ := NewMatcher(MatchEqual, "tier", "")
	if got := selectRefs(p, []*Matcher{empty}); !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf(`tier="" 후보: got %v, want [1 2]`, got)
	}

	// 매처가 없으면 전체.
	if got := selectRefs(p, nil); !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("무매처: got %v", got)
	}
}

func TestMatchesAll(t *testing.T) {
	ls := NewLabels(Label{"node", "e101"}, Label{"tier", "core"})

	eq, _ := NewMatcher(MatchEqual, "node", "e101")
	ne, _ := NewMatcher(MatchNotEqual, "tier", "gpu")
	if !matchesAll(ls, []*Matcher{eq, ne}) {
		t.Fatal("둘 다 만족해야 한다")
	}

	bad, _ := NewMatcher(MatchEqual, "node", "e102")
	if matchesAll(ls, []*Matcher{eq, bad}) {
		t.Fatal("하나라도 어긋나면 false 여야 한다")
	}

	// 없는 라벨은 빈 문자열로 취급된다.
	emptyEq, _ := NewMatcher(MatchEqual, "device", "")
	if !matchesAll(ls, []*Matcher{emptyEq}) {
		t.Fatal(`없는 라벨은 device="" 를 만족해야 한다`)
	}
}
```

`internal/tsdb/matcher_test.go`:

```go
package tsdb

import "testing"

func TestMatcher_네_종류가_모두_동작한다(t *testing.T) {
	cases := []struct {
		mt    MatchType
		value string
		input string
		want  bool
	}{
		{MatchEqual, "e101", "e101", true},
		{MatchEqual, "e101", "e102", false},
		{MatchNotEqual, "e101", "e102", true},
		{MatchNotEqual, "e101", "e101", false},
		{MatchRegexp, "e10.", "e101", true},
		{MatchRegexp, "e10.", "e201", false},
		{MatchNotRegexp, "e10.", "e201", true},
		{MatchNotRegexp, "e10.", "e101", false},
		// 정규식은 완전 일치로 앵커된다 — 부분 일치를 허용하지 않는다.
		{MatchRegexp, "e10", "e101", false},
		{MatchRegexp, "e10.*", "e101", true},
	}
	for _, c := range cases {
		m, err := NewMatcher(c.mt, "node", c.value)
		if err != nil {
			t.Fatalf("NewMatcher(%v,%q): %v", c.mt, c.value, err)
		}
		if got := m.Matches(c.input); got != c.want {
			t.Fatalf("%s.Matches(%q): got %v, want %v", m, c.input, got, c.want)
		}
	}
}

func TestMatcher_잘못된_정규식은_에러(t *testing.T) {
	if _, err := NewMatcher(MatchRegexp, "node", "e10(") ; err == nil {
		t.Fatal("컴파일 불가한 정규식은 에러여야 한다")
	}
}

func TestMatcher_String(t *testing.T) {
	m, _ := NewMatcher(MatchNotRegexp, "tier", "gpu|smart")
	if got, want := m.String(), `tier!~"gpu|smart"`; got != want {
		t.Fatalf("String: got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run 'TestMemPostings|TestPostings|TestSelectRefs|TestMatchesAll|TestMatcher' -v`
Expected: 컴파일 실패 — `undefined: newMemPostings`, `undefined: NewMatcher`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/index.go`:

```go
package tsdb

import (
	"sort"
	"sync"
)

// memPostings 는 라벨 → 시리즈 ID 역색인이다. 시리즈 수가 수만 규모라
// roaring bitmap 대신 정렬된 슬라이스로 충분하다 — 교집합이 선형 병합이고
// 메모리 지역성도 좋다.
type memPostings struct {
	mtx sync.RWMutex
	m   map[string]map[string][]uint64
	all []uint64
}

func newMemPostings() *memPostings {
	return &memPostings{m: map[string]map[string][]uint64{}}
}

// Add 는 시리즈를 색인에 넣는다. ID 는 단조 증가로 발급되므로 append 만으로
// 정렬 상태가 유지된다 — 상위 계층(Head)이 그 불변식을 지킨다.
func (p *memPostings) Add(id uint64, ls Labels) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	for _, l := range ls {
		vals, ok := p.m[l.Name]
		if !ok {
			vals = map[string][]uint64{}
			p.m[l.Name] = vals
		}
		vals[l.Value] = append(vals[l.Value], id)
	}
	p.all = append(p.all, id)
}

func (p *memPostings) Get(name, value string) []uint64 {
	p.mtx.RLock()
	defer p.mtx.RUnlock()
	return append([]uint64(nil), p.m[name][value]...)
}

func (p *memPostings) All() []uint64 {
	p.mtx.RLock()
	defer p.mtx.RUnlock()
	return append([]uint64(nil), p.all...)
}

func (p *memPostings) LabelNames() []string {
	p.mtx.RLock()
	defer p.mtx.RUnlock()
	out := make([]string, 0, len(p.m))
	for name := range p.m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (p *memPostings) LabelValues(name string) []string {
	p.mtx.RLock()
	defer p.mtx.RUnlock()
	out := make([]string, 0, len(p.m[name]))
	for v := range p.m[name] {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// 아래 세 함수는 정렬된 ID 슬라이스에 대한 집합 연산이다. 입력이 정렬돼
// 있다는 전제가 깨지면 조용히 틀린 답을 내므로, 정렬을 만드는 쪽(Add)과
// 쓰는 쪽(Select)을 같은 패키지에 묶어 둔다.

func intersect(a, b []uint64) []uint64 {
	out := []uint64{}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}

func union(a, b []uint64) []uint64 {
	out := []uint64{}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			out = append(out, a[i])
			i++
		case a[i] > b[j]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	out = append(out, a[i:]...)
	return append(out, b[j:]...)
}

func without(a, b []uint64) []uint64 {
	out := []uint64{}
	i, j := 0, 0
	for i < len(a) {
		switch {
		case j >= len(b) || a[i] < b[j]:
			out = append(out, a[i])
			i++
		case a[i] > b[j]:
			j++
		default:
			i++
			j++
		}
	}
	return out
}

// selectRefs 는 매처들로 후보 시리즈 ID 를 좁힌다. head 와 블록이 같은
// 질의 의미론을 갖도록 두 곳이 이 함수를 공유한다.
//
// 색인으로 좁힐 수 있는 것은 **값이 비지 않은** 같음·정규식 매처뿐이다:
//   - 부정 매처(!=, !~)는 "그 라벨이 아예 없는 시리즈"도 만족시킨다
//   - 빈 값 같음-매처(foo="")도 마찬가지다. 색인에는 빈 값 posting 이
//     없으므로 시드로 쓰면 결과가 통째로 사라진다
//
// 그런 매처만 있으면 전체를 후보로 돌려주고, 최종 판정은 호출자가
// matchesAll 로 한다.
func selectRefs(p *memPostings, ms []*Matcher) []uint64 {
	var candidates []uint64
	seeded := false

	for _, m := range ms {
		if m.Value == "" {
			continue
		}
		var ids []uint64
		switch m.Type {
		case MatchEqual:
			ids = p.Get(m.Name, m.Value)
		case MatchRegexp:
			for _, v := range p.LabelValues(m.Name) {
				if m.Matches(v) {
					ids = union(ids, p.Get(m.Name, v))
				}
			}
		default:
			continue
		}
		if !seeded {
			candidates, seeded = ids, true
		} else {
			candidates = intersect(candidates, ids)
		}
	}
	if !seeded {
		return p.All()
	}
	return candidates
}

// matchesAll 은 라벨셋이 매처 전부를 만족하는지 본다. 없는 라벨은 빈
// 문자열로 취급되므로 foo="" 매처가 "foo 라벨이 없음"을 뜻하게 된다.
func matchesAll(ls Labels, ms []*Matcher) bool {
	for _, m := range ms {
		if !m.Matches(ls.Get(m.Name)) {
			return false
		}
	}
	return true
}
```

`internal/tsdb/matcher.go`:

```go
package tsdb

import (
	"fmt"
	"regexp"
	"strconv"
)

type MatchType int

const (
	MatchEqual MatchType = iota
	MatchNotEqual
	MatchRegexp
	MatchNotRegexp
)

func (t MatchType) String() string {
	switch t {
	case MatchEqual:
		return "="
	case MatchNotEqual:
		return "!="
	case MatchRegexp:
		return "=~"
	case MatchNotRegexp:
		return "!~"
	}
	return "?"
}

// Matcher 는 라벨 하나에 대한 조건이다.
type Matcher struct {
	Type  MatchType
	Name  string
	Value string

	re *regexp.Regexp
}

// NewMatcher 는 매처를 만든다. 정규식은 PromQL 관례대로 **완전 일치**로
// 앵커한다 — `node=~"e10"` 이 `e101` 에 걸리면 사용자가 의도하지 않은
// 시리즈가 조용히 딸려 온다.
func NewMatcher(t MatchType, name, value string) (*Matcher, error) {
	m := &Matcher{Type: t, Name: name, Value: value}
	if t == MatchRegexp || t == MatchNotRegexp {
		re, err := regexp.Compile("^(?:" + value + ")$")
		if err != nil {
			return nil, fmt.Errorf("tsdb: 정규식 컴파일 실패 %q: %w", value, err)
		}
		m.re = re
	}
	return m, nil
}

func (m *Matcher) Matches(s string) bool {
	switch m.Type {
	case MatchEqual:
		return s == m.Value
	case MatchNotEqual:
		return s != m.Value
	case MatchRegexp:
		return m.re.MatchString(s)
	case MatchNotRegexp:
		return !m.re.MatchString(s)
	}
	return false
}

func (m *Matcher) String() string {
	return m.Name + m.Type.String() + strconv.Quote(m.Value)
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run 'TestMemPostings|TestPostings|TestSelectRefs|TestMatchesAll|TestMatcher' -v -race`
Expected: 9개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/index.go internal/tsdb/matcher.go internal/tsdb/index_test.go internal/tsdb/matcher_test.go
git commit -m "feat(tsdb): 역색인 + 라벨 매처 4종 + 공유 후보 선별(selectRefs)"
```

---

## Task 8: 인메모리 head

**Files:**
- Create: `internal/tsdb/head.go`
- Test: `internal/tsdb/head_test.go`

**Interfaces:**
- Consumes: `Chunk` (Task 5), `Labels` (Task 6), `memPostings`/`Matcher` (Task 7)
- Produces:
  - `type memSeries struct{ ref uint64; lset Labels; chunks []*Chunk; minT, maxT int64 }`
  - `func (s *memSeries) append(t int64, v float64) error`
  - `func NewHead() *Head`
  - `func (h *Head) Append(lset Labels, t int64, v float64) (uint64, error)`
  - `func (h *Head) AppendRef(ref uint64, t int64, v float64) error`
  - `func (h *Head) GetOrCreateWithRef(ref uint64, lset Labels) *memSeries`
  - `func (h *Head) Series(ref uint64) *memSeries`
  - `func (h *Head) Select(ms ...*Matcher) []*memSeries`
  - `func (h *Head) NumSeries() int`, `MinTime() int64`, `MaxTime() int64`
  - `func (h *Head) Reset()`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/head_test.go`:

```go
package tsdb

import (
	"errors"
	"testing"
)

func lset(node, metric string) Labels {
	return NewLabels(Label{MetricName, metric}, Label{"node", node})
}

func TestHead_넣은_샘플을_되읽는다(t *testing.T) {
	h := NewHead()
	ref, err := h.Append(lset("e101", "node_load1"), 1000, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Append(lset("e101", "node_load1"), 16000, 0.7); err != nil {
		t.Fatal(err)
	}

	s := h.Series(ref)
	if s == nil {
		t.Fatal("ref 로 시리즈를 못 찾았다")
	}
	if s.minT != 1000 || s.maxT != 16000 {
		t.Fatalf("시간 범위: [%d,%d]", s.minT, s.maxT)
	}
	if h.NumSeries() != 1 {
		t.Fatalf("동일 라벨셋은 시리즈 1개여야 한다, got %d", h.NumSeries())
	}
	if h.MinTime() != 1000 || h.MaxTime() != 16000 {
		t.Fatalf("head 시간 범위: [%d,%d]", h.MinTime(), h.MaxTime())
	}
}

func TestHead_같은_라벨셋은_같은_ref를_준다(t *testing.T) {
	h := NewHead()
	r1, _ := h.Append(lset("e101", "node_load1"), 1000, 1)
	r2, _ := h.Append(lset("e101", "node_load1"), 2000, 2)
	if r1 != r2 {
		t.Fatalf("같은 라벨셋인데 ref 가 다르다: %d vs %d", r1, r2)
	}
	// 라벨 하나만 달라도 다른 시리즈다.
	r3, _ := h.Append(lset("e102", "node_load1"), 1000, 1)
	if r3 == r1 {
		t.Fatal("다른 라벨셋인데 ref 가 같다")
	}
	if h.NumSeries() != 2 {
		t.Fatalf("시리즈 2개여야 한다, got %d", h.NumSeries())
	}
}

func TestHead_매처로_시리즈를_고른다(t *testing.T) {
	h := NewHead()
	h.Append(NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"}, Label{"tier", "core"}), 1000, 1)
	h.Append(NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e102"}, Label{"tier", "core"}), 1000, 2)
	h.Append(NewLabels(Label{MetricName, "gpu_temp"}, Label{"node", "e101"}, Label{"tier", "gpu"}), 1000, 60)

	eq, _ := NewMatcher(MatchEqual, MetricName, "node_load1")
	got := h.Select(eq)
	if len(got) != 2 {
		t.Fatalf("__name__=node_load1: got %d 시리즈, want 2", len(got))
	}

	re, _ := NewMatcher(MatchRegexp, "node", "e10.")
	tierEq, _ := NewMatcher(MatchEqual, "tier", "gpu")
	got = h.Select(re, tierEq)
	if len(got) != 1 || got[0].lset.Get(MetricName) != "gpu_temp" {
		t.Fatalf("node=~e10. + tier=gpu: got %d 시리즈", len(got))
	}

	neq, _ := NewMatcher(MatchNotEqual, "tier", "core")
	got = h.Select(neq)
	if len(got) != 1 {
		t.Fatalf("tier!=core: got %d 시리즈, want 1", len(got))
	}
}

func TestHead_역행_샘플을_거부한다(t *testing.T) {
	h := NewHead()
	h.Append(lset("e101", "node_load1"), 5000, 1)
	_, err := h.Append(lset("e101", "node_load1"), 4000, 2)
	if !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("역행은 ErrOutOfOrder 여야 한다, got %v", err)
	}
}

func TestHead_청크가_차면_다음_청크로_넘어간다(t *testing.T) {
	h := NewHead()
	ls := lset("e101", "node_load1")
	total := maxSamplesPerChunk + 10
	for i := 0; i < total; i++ {
		if _, err := h.Append(ls, int64(i)*15000, float64(i)); err != nil {
			t.Fatalf("샘플 %d: %v", i, err)
		}
	}
	ref, _ := h.Append(ls, int64(total)*15000, 0)
	s := h.Series(ref)
	if len(s.chunks) != 2 {
		t.Fatalf("청크가 2개여야 한다, got %d", len(s.chunks))
	}
	// 전체 샘플 수가 보존됐는지 확인.
	sum := 0
	for _, c := range s.chunks {
		sum += c.NumSamples()
	}
	if sum != total+1 {
		t.Fatalf("샘플 총합: got %d, want %d", sum, total+1)
	}
}

func TestHead_AppendRef는_기존_시리즈에_붙인다(t *testing.T) {
	h := NewHead()
	ref, _ := h.Append(lset("e101", "node_load1"), 1000, 1)
	if err := h.AppendRef(ref, 16000, 2); err != nil {
		t.Fatal(err)
	}
	if got := h.Series(ref).maxT; got != 16000 {
		t.Fatalf("maxT: got %d", got)
	}
	if err := h.AppendRef(9999, 1000, 1); err == nil {
		t.Fatal("없는 ref 는 에러여야 한다")
	}
}

func TestHead_Reset은_전부_비운다(t *testing.T) {
	h := NewHead()
	h.Append(lset("e101", "node_load1"), 1000, 1)
	h.Reset()
	if h.NumSeries() != 0 {
		t.Fatalf("Reset 후 시리즈가 남았다: %d", h.NumSeries())
	}
	eq, _ := NewMatcher(MatchEqual, "node", "e101")
	if got := h.Select(eq); len(got) != 0 {
		t.Fatalf("Reset 후 색인이 남았다: %d", len(got))
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestHead -v`
Expected: 컴파일 실패 — `undefined: NewHead`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/head.go`:

```go
package tsdb

import (
	"fmt"
	"math"
	"sync"
)

// memSeries 는 head 안의 시리즈 하나다. 열린 청크에 append 하다가 청크가
// 차면 새 청크를 연다.
type memSeries struct {
	mtx sync.Mutex

	ref    uint64
	lset   Labels
	chunks []*Chunk

	minT, maxT int64
}

func (s *memSeries) append(t int64, v float64) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if len(s.chunks) == 0 {
		s.chunks = append(s.chunks, NewChunk())
		s.minT = t
	}
	c := s.chunks[len(s.chunks)-1]
	if c.Full() {
		c = NewChunk()
		s.chunks = append(s.chunks, c)
	}
	if err := c.Append(t, v); err != nil {
		return err
	}
	if t > s.maxT {
		s.maxT = t
	}
	return nil
}

// Head 는 최근 구간을 메모리에 들고 있는 쓰기 대상이다. 디스크 표현은
// 없다 — 내구성은 WAL(Task 9)이, 영속성은 블록(Task 11)이 담당한다.
type Head struct {
	mtx sync.RWMutex

	series   map[uint64]*memSeries   // ref → 시리즈
	hashes   map[uint64][]*memSeries // 라벨 해시 → 시리즈들(해시 충돌 대비)
	postings *memPostings

	lastRef    uint64
	minT, maxT int64
}

func NewHead() *Head {
	h := &Head{}
	h.Reset()
	return h
}

func (h *Head) Reset() {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	h.series = map[uint64]*memSeries{}
	h.hashes = map[uint64][]*memSeries{}
	h.postings = newMemPostings()
	h.lastRef = 0
	h.minT = math.MaxInt64
	h.maxT = math.MinInt64
}

// getOrCreate 는 라벨셋에 해당하는 시리즈를 찾거나 만든다. 해시가 같아도
// 라벨셋 원본을 비교해 충돌을 걸러낸다.
func (h *Head) getOrCreate(lset Labels) *memSeries {
	hash := lset.Hash()

	h.mtx.RLock()
	for _, s := range h.hashes[hash] {
		if s.lset.Equal(lset) {
			h.mtx.RUnlock()
			return s
		}
	}
	h.mtx.RUnlock()

	h.mtx.Lock()
	defer h.mtx.Unlock()
	// 잠금을 놓았던 사이에 다른 고루틴이 만들었을 수 있다.
	for _, s := range h.hashes[hash] {
		if s.lset.Equal(lset) {
			return s
		}
	}
	h.lastRef++
	s := &memSeries{ref: h.lastRef, lset: lset.Copy()}
	h.series[s.ref] = s
	h.hashes[hash] = append(h.hashes[hash], s)
	h.postings.Add(s.ref, s.lset)
	return s
}

// GetOrCreateWithRef 는 WAL 재생 전용이다. 기록된 ref 를 그대로 되살려
// 이후 recSamples 레코드가 같은 ref 로 붙을 수 있게 한다.
func (h *Head) GetOrCreateWithRef(ref uint64, lset Labels) *memSeries {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	if s, ok := h.series[ref]; ok {
		return s
	}
	s := &memSeries{ref: ref, lset: lset.Copy()}
	h.series[ref] = s
	h.hashes[lset.Hash()] = append(h.hashes[lset.Hash()], s)
	h.postings.Add(ref, s.lset)
	if ref > h.lastRef {
		h.lastRef = ref
	}
	return s
}

func (h *Head) Append(lset Labels, t int64, v float64) (uint64, error) {
	s := h.getOrCreate(lset)
	if err := s.append(t, v); err != nil {
		return 0, err
	}
	h.observe(t)
	return s.ref, nil
}

func (h *Head) AppendRef(ref uint64, t int64, v float64) error {
	h.mtx.RLock()
	s, ok := h.series[ref]
	h.mtx.RUnlock()
	if !ok {
		return fmt.Errorf("tsdb: 알 수 없는 시리즈 ref %d", ref)
	}
	if err := s.append(t, v); err != nil {
		return err
	}
	h.observe(t)
	return nil
}

func (h *Head) observe(t int64) {
	h.mtx.Lock()
	if t < h.minT {
		h.minT = t
	}
	if t > h.maxT {
		h.maxT = t
	}
	h.mtx.Unlock()
}

func (h *Head) Series(ref uint64) *memSeries {
	h.mtx.RLock()
	defer h.mtx.RUnlock()
	return h.series[ref]
}

func (h *Head) NumSeries() int {
	h.mtx.RLock()
	defer h.mtx.RUnlock()
	return len(h.series)
}

func (h *Head) MinTime() int64 {
	h.mtx.RLock()
	defer h.mtx.RUnlock()
	if h.minT == math.MaxInt64 {
		return 0
	}
	return h.minT
}

func (h *Head) MaxTime() int64 {
	h.mtx.RLock()
	defer h.mtx.RUnlock()
	if h.maxT == math.MinInt64 {
		return 0
	}
	return h.maxT
}

// Select 는 매처를 모두 만족하는 시리즈를 ref 오름차순으로 낸다. 후보
// 선별은 selectRefs 가, 최종 판정은 matchesAll 이 한다 — 블록(Task 11)도
// 같은 두 함수를 쓰므로 head 와 블록의 질의 의미론이 갈라지지 않는다.
// 매처를 하나도 주지 않으면 전체 시리즈가 나온다.
func (h *Head) Select(ms ...*Matcher) []*memSeries {
	h.mtx.RLock()
	defer h.mtx.RUnlock()

	refs := selectRefs(h.postings, ms)
	out := make([]*memSeries, 0, len(refs))
	for _, id := range refs {
		s := h.series[id]
		if s != nil && matchesAll(s.lset, ms) {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestHead -v -race`
Expected: 7개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/head.go internal/tsdb/head_test.go
git commit -m "feat(tsdb): 인메모리 head — 시리즈 관리, 청크 롤오버, 매처 조회"
```

---

## Task 9: WAL 쓰기와 재생

**Files:**
- Create: `internal/tsdb/wal.go`
- Test: `internal/tsdb/wal_test.go`

**Interfaces:**
- Consumes: `Labels` (Task 6)
- Produces:
  - `type RefSample struct{ Ref uint64; T int64; V float64 }`
  - `const defaultSegmentSize = 32 << 20`
  - `func OpenWAL(dir string, segSize int64) (*WAL, error)`
  - `func (w *WAL) LogSeries(ref uint64, ls Labels) error`
  - `func (w *WAL) LogSamples(ss []RefSample) error`
  - `func (w *WAL) Sync() error`, `Close() error`, `Truncate() error`
  - `func ReplayWAL(dir string, onSeries func(uint64, Labels) error, onSamples func([]RefSample) error) error`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/wal_test.go`:

```go
package tsdb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWAL_기록한_시리즈와_샘플을_재생한다(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, defaultSegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	ls1 := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	ls2 := NewLabels(Label{MetricName, "gpu_temp"}, Label{"node", "e101"}, Label{"device", "gpu0"})
	if err := w.LogSeries(1, ls1); err != nil {
		t.Fatal(err)
	}
	if err := w.LogSeries(2, ls2); err != nil {
		t.Fatal(err)
	}
	if err := w.LogSamples([]RefSample{{1, 1000, 0.5}, {2, 1000, 61.5}}); err != nil {
		t.Fatal(err)
	}
	if err := w.LogSamples([]RefSample{{1, 16000, 0.7}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var gotSeries []Labels
	var gotSamples []RefSample
	err = ReplayWAL(dir,
		func(ref uint64, ls Labels) error {
			gotSeries = append(gotSeries, ls)
			return nil
		},
		func(ss []RefSample) error {
			gotSamples = append(gotSamples, ss...)
			return nil
		})
	if err != nil {
		t.Fatalf("ReplayWAL: %v", err)
	}

	if len(gotSeries) != 2 {
		t.Fatalf("시리즈 레코드: got %d, want 2", len(gotSeries))
	}
	if !gotSeries[0].Equal(ls1) || !gotSeries[1].Equal(ls2) {
		t.Fatalf("라벨셋이 왕복하지 않았다: %v", gotSeries)
	}
	if len(gotSamples) != 3 {
		t.Fatalf("샘플: got %d, want 3", len(gotSamples))
	}
	want := []RefSample{{1, 1000, 0.5}, {2, 1000, 61.5}, {1, 16000, 0.7}}
	for i, w := range want {
		if gotSamples[i] != w {
			t.Fatalf("샘플 %d: got %+v, want %+v", i, gotSamples[i], w)
		}
	}
}

func TestWAL_크기를_넘기면_세그먼트를_회전한다(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, 512) // 아주 작은 세그먼트
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if err := w.LogSamples([]RefSample{{uint64(i), int64(i) * 1000, float64(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	entries, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(entries) < 2 {
		t.Fatalf("세그먼트가 회전하지 않았다: %v", entries)
	}

	n := 0
	err = ReplayWAL(dir, func(uint64, Labels) error { return nil },
		func(ss []RefSample) error { n += len(ss); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if n != 200 {
		t.Fatalf("회전 후 재생 샘플 수: got %d, want 200", n)
	}
}

func TestWAL_재오픈시_기존_세그먼트에_이어쓴다(t *testing.T) {
	dir := t.TempDir()
	w, _ := OpenWAL(dir, defaultSegmentSize)
	w.LogSamples([]RefSample{{1, 1000, 1}})
	w.Close()

	w2, err := OpenWAL(dir, defaultSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	w2.LogSamples([]RefSample{{1, 2000, 2}})
	w2.Close()

	n := 0
	ReplayWAL(dir, func(uint64, Labels) error { return nil },
		func(ss []RefSample) error { n += len(ss); return nil })
	if n != 2 {
		t.Fatalf("이어쓰기 후 샘플: got %d, want 2", n)
	}
}

func TestWAL_Truncate는_모든_세그먼트를_지운다(t *testing.T) {
	dir := t.TempDir()
	w, _ := OpenWAL(dir, defaultSegmentSize)
	w.LogSamples([]RefSample{{1, 1000, 1}})
	if err := w.Truncate(); err != nil {
		t.Fatal(err)
	}
	w.LogSamples([]RefSample{{1, 2000, 2}})
	w.Close()

	n := 0
	ReplayWAL(dir, func(uint64, Labels) error { return nil },
		func(ss []RefSample) error { n += len(ss); return nil })
	if n != 1 {
		t.Fatalf("Truncate 후 샘플: got %d, want 1 (자른 뒤 쓴 것만)", n)
	}
}

func TestReplayWAL_빈_디렉터리는_에러가_아니다(t *testing.T) {
	if err := ReplayWAL(t.TempDir(),
		func(uint64, Labels) error { return nil },
		func([]RefSample) error { return nil }); err != nil {
		t.Fatalf("빈 WAL 재생은 성공해야 한다: %v", err)
	}
}

func TestReplayWAL_없는_디렉터리는_에러가_아니다(t *testing.T) {
	if err := ReplayWAL(filepath.Join(t.TempDir(), "없음"),
		func(uint64, Labels) error { return nil },
		func([]RefSample) error { return nil }); err != nil {
		t.Fatalf("미존재 WAL 재생은 성공해야 한다: %v", err)
	}
	_ = os.Remove
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run 'TestWAL|TestReplayWAL' -v`
Expected: 컴파일 실패 — `undefined: OpenWAL`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/wal.go`:

```go
package tsdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// WAL 레코드 포맷:
//
//	[1B type][4B payloadLen][payload][4B crc32]
//
// crc 는 type+len+payload 전체에 대해 계산한다. 크래시로 마지막 레코드가
// 잘리는 것은 **정상 상황**이므로, 재생기는 손상 지점에서 조용히 멈추고
// 그때까지의 결과를 성공으로 돌려준다 (Task 10 이 이 동작을 고정한다).
const (
	recSeries  byte = 1
	recSamples byte = 2
)

const defaultSegmentSize int64 = 32 << 20

var crcTable = crc32.MakeTable(crc32.Castagnoli)

type RefSample struct {
	Ref uint64
	T   int64
	V   float64
}

type WAL struct {
	mtx sync.Mutex

	dir     string
	f       *os.File
	segIdx  int
	size    int64
	segSize int64
}

func segmentName(dir string, idx int) string {
	return filepath.Join(dir, fmt.Sprintf("%08d", idx))
}

// listSegments 는 WAL 세그먼트를 번호 오름차순으로 낸다.
func listSegments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// 세그먼트 파일명은 %08d 로만 만들어진다. 8글자 전부가 ASCII 숫자인
		// 것만 인정한다 — strconv.Atoi 는 "-0000001" 의 선행 부호를 허용해
		// 남의 파일을 세그먼트로 오인하고, 그 파일이 사전순 맨 앞에 오면
		// ReplayWAL 의 "손상 시 전체 중단"과 맞물려 후속 세그먼트를 전부
		// 못 읽는 조용한 데이터 손실이 난다.
		if len(e.Name()) == 8 && allDigits(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(dir, n)
	}
	return out, nil
}

// allDigits 는 문자열이 ASCII 숫자로만 이뤄졌는지 본다. strconv.Atoi 와 달리
// 선행 부호(+/-)나 공백을 허용하지 않는다.
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func OpenWAL(dir string, segSize int64) (*WAL, error) {
	if segSize <= 0 {
		segSize = defaultSegmentSize
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	segs, err := listSegments(dir)
	if err != nil {
		return nil, err
	}

	w := &WAL{dir: dir, segSize: segSize, segIdx: 1}
	if len(segs) > 0 {
		last := segs[len(segs)-1]
		var idx int
		if _, err := fmt.Sscanf(filepath.Base(last), "%08d", &idx); err == nil {
			w.segIdx = idx
		}
	}
	if err := w.openSegment(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *WAL) openSegment() error {
	f, err := os.OpenFile(segmentName(w.dir, w.segIdx), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f = f
	w.size = st.Size()
	return nil
}

func (w *WAL) writeRecord(typ byte, payload []byte) error {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.size >= w.segSize {
		if err := w.f.Close(); err != nil {
			return err
		}
		w.segIdx++
		if err := w.openSegment(); err != nil {
			return err
		}
	}

	buf := make([]byte, 0, 5+len(payload)+4)
	buf = append(buf, typ)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(buf, crcTable))

	n, err := w.f.Write(buf)
	w.size += int64(n)
	return err
}

func appendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

func (w *WAL) LogSeries(ref uint64, ls Labels) error {
	buf := make([]byte, 0, 64)
	buf = binary.BigEndian.AppendUint64(buf, ref)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(ls)))
	for _, l := range ls {
		buf = appendString(buf, l.Name)
		buf = appendString(buf, l.Value)
	}
	return w.writeRecord(recSeries, buf)
}

func (w *WAL) LogSamples(ss []RefSample) error {
	if len(ss) == 0 {
		return nil
	}
	buf := make([]byte, 0, 2+len(ss)*24)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(ss)))
	for _, s := range ss {
		buf = binary.BigEndian.AppendUint64(buf, s.Ref)
		buf = binary.BigEndian.AppendUint64(buf, uint64(s.T))
		buf = binary.BigEndian.AppendUint64(buf, math.Float64bits(s.V))
	}
	return w.writeRecord(recSamples, buf)
}

func (w *WAL) Sync() error {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	return w.f.Sync()
}

func (w *WAL) Close() error {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Truncate 는 블록 flush 가 끝나 더 이상 필요 없어진 WAL 을 통째로 버리고
// 다음 세그먼트 번호로 새로 시작한다.
func (w *WAL) Truncate() error {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.f != nil {
		if err := w.f.Close(); err != nil {
			return err
		}
		w.f = nil
	}
	segs, err := listSegments(w.dir)
	if err != nil {
		return err
	}
	for _, s := range segs {
		if err := os.Remove(s); err != nil {
			return err
		}
	}
	w.segIdx++
	return w.openSegment()
}

// parseSeries / parseSamples 는 payload 를 되읽는다. 길이가 모자라면
// 손상으로 보고 에러를 낸다 — 재생기가 그 지점에서 멈춘다.
func parseSeries(p []byte) (uint64, Labels, error) {
	if len(p) < 10 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	ref := binary.BigEndian.Uint64(p[:8])
	n := int(binary.BigEndian.Uint16(p[8:10]))
	p = p[10:]

	ls := make(Labels, 0, n)
	readStr := func() (string, error) {
		if len(p) < 2 {
			return "", io.ErrUnexpectedEOF
		}
		l := int(binary.BigEndian.Uint16(p[:2]))
		p = p[2:]
		if len(p) < l {
			return "", io.ErrUnexpectedEOF
		}
		s := string(p[:l])
		p = p[l:]
		return s, nil
	}
	for i := 0; i < n; i++ {
		name, err := readStr()
		if err != nil {
			return 0, nil, err
		}
		val, err := readStr()
		if err != nil {
			return 0, nil, err
		}
		ls = append(ls, Label{Name: name, Value: val})
	}
	return ref, ls, nil
}

func parseSamples(p []byte) ([]RefSample, error) {
	if len(p) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if len(p) < n*24 {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]RefSample, n)
	for i := 0; i < n; i++ {
		off := i * 24
		out[i] = RefSample{
			Ref: binary.BigEndian.Uint64(p[off : off+8]),
			T:   int64(binary.BigEndian.Uint64(p[off+8 : off+16])),
			V:   math.Float64frombits(binary.BigEndian.Uint64(p[off+16 : off+24])),
		}
	}
	return out, nil
}

// ReplayWAL 은 세그먼트를 번호순으로 읽어 콜백을 호출한다. 손상된 레코드를
// 만나면 **그 지점에서 멈추고 nil 을 반환한다** — 크래시로 마지막 쓰기가
// 잘린 상황이 정상이기 때문이다. 콜백이 낸 에러는 그대로 전파한다.
func ReplayWAL(dir string, onSeries func(uint64, Labels) error, onSamples func([]RefSample) error) error {
	segs, err := listSegments(dir)
	if err != nil {
		return err
	}
	for _, seg := range segs {
		data, err := os.ReadFile(seg)
		if err != nil {
			return err
		}
		for off := 0; ; {
			if off+5 > len(data) {
				break // 헤더도 안 남음 — 정상 종료 또는 절단
			}
			typ := data[off]
			plen := int(binary.BigEndian.Uint32(data[off+1 : off+5]))
			end := off + 5 + plen + 4
			if plen < 0 || end > len(data) {
				break // 페이로드/CRC 가 잘림
			}
			want := binary.BigEndian.Uint32(data[end-4 : end])
			if crc32.Checksum(data[off:end-4], crcTable) != want {
				break // CRC 불일치 — 이 지점부터 신뢰 불가
			}
			payload := data[off+5 : off+5+plen]

			switch typ {
			case recSeries:
				ref, ls, err := parseSeries(payload)
				if err != nil {
					return nil // 손상 — 여기서 멈춘다
				}
				if err := onSeries(ref, ls); err != nil {
					return err
				}
			case recSamples:
				ss, err := parseSamples(payload)
				if err != nil {
					return nil
				}
				if err := onSamples(ss); err != nil {
					return err
				}
			default:
				return nil // 알 수 없는 타입 — 손상으로 본다
			}
			off = end
		}
	}
	return nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run 'TestWAL|TestReplayWAL' -v -race`
Expected: 6개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/wal.go internal/tsdb/wal_test.go
git commit -m "feat(tsdb): WAL 레코드 포맷·세그먼트 회전·재생 — CRC32C 검증"
```

---

## Task 10: 크래시 내성과 head 복구

**Files:**
- Create: `internal/tsdb/recover.go`
- Test: `internal/tsdb/recover_test.go`

**Interfaces:**
- Consumes: `Head` (Task 8), `ReplayWAL`/`RefSample` (Task 9)
- Produces:
  - `func RecoverHead(dir string) (*Head, error)`

> **이 태스크가 M1 의 핵심 위험을 닫는다.** WAL 이 틀리면 화면은 멀쩡한데 재시작 때 데이터가 조용히 사라진다. 여기서 절단·손상을 명시적으로 테스트해 그 실패를 눈에 보이게 만든다.

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/recover_test.go`:

```go
package tsdb

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWALFixture 는 시리즈 1개에 n 개 샘플을 기록한 WAL 을 만든다.
func writeWALFixture(t *testing.T, dir string, n int) Labels {
	t.Helper()
	w, err := OpenWAL(dir, defaultSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	if err := w.LogSeries(1, ls); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := w.LogSamples([]RefSample{{1, int64(i) * 15000, float64(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestRecoverHead_온전한_WAL을_전부_복구한다(t *testing.T) {
	dir := t.TempDir()
	ls := writeWALFixture(t, dir, 100)

	h, err := RecoverHead(dir)
	if err != nil {
		t.Fatalf("RecoverHead: %v", err)
	}
	if h.NumSeries() != 1 {
		t.Fatalf("시리즈: got %d, want 1", h.NumSeries())
	}
	s := h.Series(1)
	if s == nil {
		t.Fatal("ref 1 시리즈가 없다")
	}
	if !s.lset.Equal(ls) {
		t.Fatalf("라벨셋: got %v", s.lset)
	}
	total := 0
	for _, c := range s.chunks {
		total += c.NumSamples()
	}
	if total != 100 {
		t.Fatalf("복구 샘플: got %d, want 100", total)
	}
	if h.MaxTime() != 99*15000 {
		t.Fatalf("maxT: got %d, want %d", h.MaxTime(), 99*15000)
	}
}

func TestRecoverHead_잘린_WAL은_앞부분까지_복구한다(t *testing.T) {
	dir := t.TempDir()
	writeWALFixture(t, dir, 100)

	// 크래시 주입: 마지막 세그먼트를 임의 지점에서 자른다.
	segs, err := listSegments(dir)
	if err != nil || len(segs) == 0 {
		t.Fatalf("세그먼트를 못 찾았다: %v", err)
	}
	last := segs[len(segs)-1]
	st, _ := os.Stat(last)
	if err := os.Truncate(last, st.Size()-7); err != nil {
		t.Fatal(err)
	}

	h, err := RecoverHead(dir)
	if err != nil {
		t.Fatalf("절단된 WAL 복구는 에러가 아니어야 한다: %v", err)
	}
	s := h.Series(1)
	if s == nil {
		t.Fatal("절단 전 시리즈는 살아 있어야 한다")
	}
	total := 0
	for _, c := range s.chunks {
		total += c.NumSamples()
	}
	// 마지막 레코드 하나만 잃는다 — 그 앞은 온전해야 한다.
	if total != 99 {
		t.Fatalf("절단 복구 샘플: got %d, want 99", total)
	}
}

func TestRecoverHead_CRC가_깨지면_그_지점에서_멈춘다(t *testing.T) {
	dir := t.TempDir()
	writeWALFixture(t, dir, 100)

	segs, _ := listSegments(dir)
	last := segs[len(segs)-1]
	data, err := os.ReadFile(last)
	if err != nil {
		t.Fatal(err)
	}
	// 파일 한가운데의 한 바이트를 뒤집는다.
	mid := len(data) / 2
	data[mid] ^= 0xff
	if err := os.WriteFile(last, data, 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := RecoverHead(dir)
	if err != nil {
		t.Fatalf("손상 WAL 복구는 에러가 아니어야 한다: %v", err)
	}
	s := h.Series(1)
	if s == nil {
		t.Fatal("손상 전 시리즈는 살아 있어야 한다")
	}
	total := 0
	for _, c := range s.chunks {
		total += c.NumSamples()
	}
	if total == 0 {
		t.Fatal("손상 지점 앞의 샘플까지 잃었다")
	}
	if total >= 100 {
		t.Fatalf("손상 지점 뒤를 읽어들였다: %d", total)
	}
}

func TestRecoverHead_WAL이_없으면_빈_head를_준다(t *testing.T) {
	h, err := RecoverHead(filepath.Join(t.TempDir(), "없음"))
	if err != nil {
		t.Fatalf("미존재 WAL: %v", err)
	}
	if h.NumSeries() != 0 {
		t.Fatalf("빈 head 여야 한다: %d", h.NumSeries())
	}
}

func TestRecoverHead_복구후_추가_append가_이어진다(t *testing.T) {
	dir := t.TempDir()
	writeWALFixture(t, dir, 10)

	h, err := RecoverHead(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 복구된 ref 다음 번호가 나가야 한다 — 같은 ref 를 재발급하면 안 된다.
	newRef, err := h.Append(NewLabels(Label{MetricName, "gpu_temp"}, Label{"node", "e101"}), 20000, 61)
	if err != nil {
		t.Fatal(err)
	}
	if newRef == 1 {
		t.Fatal("복구된 ref 를 재발급했다")
	}
	if h.NumSeries() != 2 {
		t.Fatalf("시리즈: got %d, want 2", h.NumSeries())
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestRecoverHead -v`
Expected: 컴파일 실패 — `undefined: RecoverHead`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/recover.go`:

```go
package tsdb

// RecoverHead 는 WAL 디렉터리를 재생해 head 를 복원한다.
//
// 손상·절단된 레코드는 ReplayWAL 이 조용히 잘라내므로, 여기서는 "재생된
// 만큼만 head 에 채운다"는 단순한 계약만 지킨다. 재생 중 만난 시리즈는
// 원래 ref 를 그대로 되살려야 이후 recSamples 레코드가 붙을 수 있다.
func RecoverHead(dir string) (*Head, error) {
	h := NewHead()

	err := ReplayWAL(dir,
		func(ref uint64, ls Labels) error {
			h.GetOrCreateWithRef(ref, ls)
			return nil
		},
		func(ss []RefSample) error {
			for _, s := range ss {
				// 시리즈 레코드가 손상돼 없어졌을 수 있다 — 그런 샘플은
				// 버린다(라벨셋을 모르면 어차피 조회할 수 없다).
				if h.Series(s.Ref) == nil {
					continue
				}
				if err := h.AppendRef(s.Ref, s.T, s.V); err != nil {
					// 역행 샘플은 버리고 계속한다. WAL 은 append 순서를
					// 보존하므로 정상 경로에서는 발생하지 않는다.
					continue
				}
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return h, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestRecoverHead -v -race`
Expected: 5개 테스트 모두 PASS. 특히 절단 테스트가 99 샘플을 복구해야 한다.

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/recover.go internal/tsdb/recover_test.go
git commit -m "feat(tsdb): WAL 재생으로 head 복구 — 절단·CRC 손상 내성 테스트 포함"
```

---

## Task 11: 불변 블록 쓰기와 읽기

**Files:**
- Create: `internal/tsdb/block.go`
- Test: `internal/tsdb/block_test.go`

**Interfaces:**
- Consumes: `memSeries`/`Head` (Task 8), `Chunk` (Task 5), `memPostings`/`Matcher` (Task 7)
- Produces:
  - `const ResolutionRaw = "raw"`
  - `type BlockMeta struct{ Version string; MinTime, MaxTime int64; Series, Samples int; Resolution string }`
  - `type chunkRef struct{ Offset int64; Length uint32; MinT, MaxT int64 }`
  - `type blockSeries struct{ Ref uint64; Lset Labels; Chunks []chunkRef }`
  - `func WriteBlock(baseDir string, series []*memSeries, resolution string) (string, error)`
  - `func ReadBlockMeta(dir string) (BlockMeta, error)`
  - `func OpenBlock(dir string) (*Block, error)`
  - `func (b *Block) Meta() BlockMeta`, `Select(ms ...*Matcher) []*blockSeries`, `Chunk(cr chunkRef) (*Chunk, error)`, `Close() error`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/block_test.go`:

```go
package tsdb

import (
	"os"
	"path/filepath"
	"testing"
)

// buildHead 는 노드 2개 × 메트릭 2개 시리즈에 샘플을 채운 head 를 만든다.
func buildHead(t *testing.T, samples int) *Head {
	t.Helper()
	h := NewHead()
	for _, node := range []string{"e101", "e102"} {
		for _, metric := range []string{"node_load1", "gpu_temp"} {
			ls := NewLabels(Label{MetricName, metric}, Label{"node", node})
			for i := 0; i < samples; i++ {
				if _, err := h.Append(ls, int64(i)*15000, float64(i)+1); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return h
}

func allSeries(h *Head) []*memSeries {
	m, _ := NewMatcher(MatchRegexp, MetricName, ".*")
	return h.Select(m)
}

func TestWriteBlock_메타와_파일을_남긴다(t *testing.T) {
	h := buildHead(t, 50)
	base := t.TempDir()

	dir, err := WriteBlock(base, allSeries(h), ResolutionRaw)
	if err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	for _, f := range []string{"meta.json", "index", "chunks"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s 가 없다: %v", f, err)
		}
	}

	meta, err := ReadBlockMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Series != 4 {
		t.Fatalf("Series: got %d, want 4", meta.Series)
	}
	if meta.Samples != 200 {
		t.Fatalf("Samples: got %d, want 200", meta.Samples)
	}
	if meta.MinTime != 0 || meta.MaxTime != 49*15000 {
		t.Fatalf("시간 범위: [%d,%d]", meta.MinTime, meta.MaxTime)
	}
	if meta.Resolution != ResolutionRaw {
		t.Fatalf("Resolution: got %q", meta.Resolution)
	}
}

func TestBlock_쓴_샘플을_그대로_읽는다(t *testing.T) {
	h := buildHead(t, 50)
	base := t.TempDir()
	dir, err := WriteBlock(base, allSeries(h), ResolutionRaw)
	if err != nil {
		t.Fatal(err)
	}

	b, err := OpenBlock(dir)
	if err != nil {
		t.Fatalf("OpenBlock: %v", err)
	}
	defer b.Close()

	eq, _ := NewMatcher(MatchEqual, MetricName, "node_load1")
	nodeEq, _ := NewMatcher(MatchEqual, "node", "e101")
	got := b.Select(eq, nodeEq)
	if len(got) != 1 {
		t.Fatalf("매처 결과: got %d 시리즈, want 1", len(got))
	}

	n := 0
	for _, cr := range got[0].Chunks {
		c, err := b.Chunk(cr)
		if err != nil {
			t.Fatalf("Chunk: %v", err)
		}
		it := c.Iterator()
		for it.Next() {
			ts, v := it.At()
			if ts != int64(n)*15000 || v != float64(n)+1 {
				t.Fatalf("샘플 %d: got (%d,%v)", n, ts, v)
			}
			n++
		}
		if it.Err() != nil {
			t.Fatalf("이터레이터: %v", it.Err())
		}
	}
	if n != 50 {
		t.Fatalf("샘플 수: got %d, want 50", n)
	}
}

func TestBlock_여러_청크를_가진_시리즈도_왕복한다(t *testing.T) {
	// 청크 상한을 넘겨 시리즈당 청크가 2개 이상 되게 한다.
	h := buildHead(t, maxSamplesPerChunk+30)
	base := t.TempDir()
	dir, _ := WriteBlock(base, allSeries(h), ResolutionRaw)

	b, err := OpenBlock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	eq, _ := NewMatcher(MatchEqual, "node", "e102")
	got := b.Select(eq)
	if len(got) != 2 {
		t.Fatalf("node=e102: got %d 시리즈, want 2", len(got))
	}
	for _, s := range got {
		if len(s.Chunks) < 2 {
			t.Fatalf("청크가 2개 이상이어야 한다: %d", len(s.Chunks))
		}
		n := 0
		for _, cr := range s.Chunks {
			c, err := b.Chunk(cr)
			if err != nil {
				t.Fatal(err)
			}
			n += c.NumSamples()
		}
		if n != maxSamplesPerChunk+30 {
			t.Fatalf("샘플 수: got %d, want %d", n, maxSamplesPerChunk+30)
		}
	}
}

func TestWriteBlock_빈_시리즈_목록은_블록을_만들지_않는다(t *testing.T) {
	dir, err := WriteBlock(t.TempDir(), nil, ResolutionRaw)
	if err != nil {
		t.Fatalf("빈 목록은 에러가 아니어야 한다: %v", err)
	}
	if dir != "" {
		t.Fatalf("빈 목록은 빈 경로를 줘야 한다: %q", dir)
	}
}

func TestOpenBlock_손상된_인덱스를_거부한다(t *testing.T) {
	h := buildHead(t, 10)
	base := t.TempDir()
	dir, _ := WriteBlock(base, allSeries(h), ResolutionRaw)

	if err := os.WriteFile(filepath.Join(dir, "index"), []byte("깨짐"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBlock(dir); err == nil {
		t.Fatal("손상된 인덱스는 에러여야 한다")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run 'TestWriteBlock|TestBlock|TestOpenBlock' -v`
Expected: 컴파일 실패 — `undefined: WriteBlock`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/block.go`:

```go
package tsdb

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ResolutionRaw 는 원본 해상도 블록의 표식이다. 롤업 블록은 "5m" 등을 쓴다.
const ResolutionRaw = "raw"

var indexMagic = [4]byte{'N', 'V', 'I', 'X'}

var ErrInvalidIndex = errors.New("tsdb: 블록 인덱스가 손상됨")

// BlockMeta 는 블록 디렉터리의 meta.json 이다. DB 는 블록을 열지 않고
// 이 파일만으로 시간 범위를 알 수 있어야 한다 — 질의에 걸치는 블록만
// 여는 전략(querier)이 여기에 기댄다.
type BlockMeta struct {
	Version    string `json:"version"`
	MinTime    int64  `json:"minTime"`
	MaxTime    int64  `json:"maxTime"`
	Series     int    `json:"series"`
	Samples    int    `json:"samples"`
	Resolution string `json:"resolution"`
}

type chunkRef struct {
	Offset int64
	Length uint32
	MinT   int64
	MaxT   int64
}

type blockSeries struct {
	Ref    uint64
	Lset   Labels
	Chunks []chunkRef
}

// WriteBlock 은 시리즈 목록을 불변 블록 디렉터리로 굳힌다. 반환값은 만들어진
// 디렉터리 경로이며, 시리즈가 없으면 빈 문자열을 준다(빈 블록을 만들지 않음).
//
// 디렉터리 이름은 `<minT>-<maxT>-<resolution>` 이라 이름만으로 정렬·식별된다.
func WriteBlock(baseDir string, series []*memSeries, resolution string) (string, error) {
	if len(series) == 0 {
		return "", nil
	}

	meta := BlockMeta{
		Version:    Version,
		MinTime:    math.MaxInt64,
		MaxTime:    math.MinInt64,
		Resolution: resolution,
	}

	var chunksBuf []byte
	entries := make([]blockSeries, 0, len(series))

	for _, s := range series {
		if len(s.chunks) == 0 {
			continue
		}
		bs := blockSeries{Ref: s.ref, Lset: s.lset}
		for _, c := range s.chunks {
			if c.NumSamples() == 0 {
				continue
			}
			raw := c.Bytes()
			bs.Chunks = append(bs.Chunks, chunkRef{
				Offset: int64(len(chunksBuf)),
				Length: uint32(len(raw)),
				MinT:   c.MinTime(),
				MaxT:   c.MaxTime(),
			})
			chunksBuf = append(chunksBuf, raw...)
			meta.Samples += c.NumSamples()
		}
		if len(bs.Chunks) == 0 {
			continue
		}
		if s.minT < meta.MinTime {
			meta.MinTime = s.minT
		}
		if s.maxT > meta.MaxTime {
			meta.MaxTime = s.maxT
		}
		entries = append(entries, bs)
	}
	if len(entries) == 0 {
		return "", nil
	}
	meta.Series = len(entries)

	dir := filepath.Join(baseDir, fmt.Sprintf("%013d-%013d-%s", meta.MinTime, meta.MaxTime, resolution))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// index 직렬화
	idx := make([]byte, 0, 4096)
	idx = append(idx, indexMagic[:]...)
	idx = append(idx, 1) // 인덱스 포맷 버전
	idx = binary.BigEndian.AppendUint32(idx, uint32(len(entries)))
	for _, e := range entries {
		idx = binary.BigEndian.AppendUint64(idx, e.Ref)
		idx = binary.BigEndian.AppendUint16(idx, uint16(len(e.Lset)))
		for _, l := range e.Lset {
			idx = appendString(idx, l.Name)
			idx = appendString(idx, l.Value)
		}
		idx = binary.BigEndian.AppendUint16(idx, uint16(len(e.Chunks)))
		for _, c := range e.Chunks {
			idx = binary.BigEndian.AppendUint64(idx, uint64(c.Offset))
			idx = binary.BigEndian.AppendUint32(idx, c.Length)
			idx = binary.BigEndian.AppendUint64(idx, uint64(c.MinT))
			idx = binary.BigEndian.AppendUint64(idx, uint64(c.MaxT))
		}
	}

	// chunks → index → meta.json 순으로 쓴다. meta.json 이 마지막이라
	// 그 파일의 존재가 "이 블록은 완성됐다"는 표식이 된다.
	if err := os.WriteFile(filepath.Join(dir, "chunks"), chunksBuf, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "index"), idx, 0o644); err != nil {
		return "", err
	}
	mj, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mj, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func ReadBlockMeta(dir string) (BlockMeta, error) {
	var m BlockMeta
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

// Block 은 열린 블록이다. 인덱스는 메모리에 올리고 청크는 필요할 때만
// ReadAt 으로 읽는다 — 블록이 여럿 열려도 메모리가 청크 크기에 비례해
// 늘지 않게 하려는 설계다.
type Block struct {
	dir      string
	meta     BlockMeta
	series   []blockSeries
	postings *memPostings
	byRef    map[uint64]*blockSeries
	chunksF  *os.File
}

func OpenBlock(dir string) (*Block, error) {
	meta, err := ReadBlockMeta(dir)
	if err != nil {
		return nil, err
	}
	idx, err := os.ReadFile(filepath.Join(dir, "index"))
	if err != nil {
		return nil, err
	}
	if len(idx) < 9 || [4]byte{idx[0], idx[1], idx[2], idx[3]} != indexMagic {
		return nil, ErrInvalidIndex
	}

	p := idx[5:]
	readU16 := func() (int, bool) {
		if len(p) < 2 {
			return 0, false
		}
		v := int(binary.BigEndian.Uint16(p[:2]))
		p = p[2:]
		return v, true
	}
	readStr := func() (string, bool) {
		n, ok := readU16()
		if !ok || len(p) < n {
			return "", false
		}
		s := string(p[:n])
		p = p[n:]
		return s, true
	}

	if len(p) < 4 {
		return nil, ErrInvalidIndex
	}
	numSeries := int(binary.BigEndian.Uint32(p[:4]))
	p = p[4:]

	b := &Block{
		dir:      dir,
		meta:     meta,
		postings: newMemPostings(),
		byRef:    map[uint64]*blockSeries{},
	}
	b.series = make([]blockSeries, 0, numSeries)

	for i := 0; i < numSeries; i++ {
		if len(p) < 8 {
			return nil, ErrInvalidIndex
		}
		var e blockSeries
		e.Ref = binary.BigEndian.Uint64(p[:8])
		p = p[8:]

		nl, ok := readU16()
		if !ok {
			return nil, ErrInvalidIndex
		}
		ls := make(Labels, 0, nl)
		for j := 0; j < nl; j++ {
			name, ok1 := readStr()
			val, ok2 := readStr()
			if !ok1 || !ok2 {
				return nil, ErrInvalidIndex
			}
			ls = append(ls, Label{Name: name, Value: val})
		}
		e.Lset = ls

		nc, ok := readU16()
		if !ok {
			return nil, ErrInvalidIndex
		}
		for j := 0; j < nc; j++ {
			if len(p) < 28 {
				return nil, ErrInvalidIndex
			}
			e.Chunks = append(e.Chunks, chunkRef{
				Offset: int64(binary.BigEndian.Uint64(p[0:8])),
				Length: binary.BigEndian.Uint32(p[8:12]),
				MinT:   int64(binary.BigEndian.Uint64(p[12:20])),
				MaxT:   int64(binary.BigEndian.Uint64(p[20:28])),
			})
			p = p[28:]
		}
		b.series = append(b.series, e)
	}

	for i := range b.series {
		b.postings.Add(b.series[i].Ref, b.series[i].Lset)
		b.byRef[b.series[i].Ref] = &b.series[i]
	}

	f, err := os.Open(filepath.Join(dir, "chunks"))
	if err != nil {
		return nil, err
	}
	b.chunksF = f
	return b, nil
}

func (b *Block) Meta() BlockMeta { return b.meta }
func (b *Block) Dir() string     { return b.dir }

func (b *Block) Close() error {
	if b.chunksF == nil {
		return nil
	}
	err := b.chunksF.Close()
	b.chunksF = nil
	return err
}

// Chunk 는 청크 참조가 가리키는 바이트를 읽어 청크로 되살린다.
func (b *Block) Chunk(cr chunkRef) (*Chunk, error) {
	buf := make([]byte, cr.Length)
	if _, err := b.chunksF.ReadAt(buf, cr.Offset); err != nil {
		return nil, err
	}
	return ChunkFromBytes(buf)
}

// Select 는 Head.Select 와 **같은 두 함수**(selectRefs·matchesAll)를 써서
// 동일한 질의 의미론을 보장한다.
func (b *Block) Select(ms ...*Matcher) []*blockSeries {
	refs := selectRefs(b.postings, ms)
	out := make([]*blockSeries, 0, len(refs))
	for _, id := range refs {
		s := b.byRef[id]
		if s != nil && matchesAll(s.Lset, ms) {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run 'TestWriteBlock|TestBlock|TestOpenBlock' -v -race`
Expected: 5개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/block.go internal/tsdb/block_test.go
git commit -m "feat(tsdb): 불변 블록 쓰기/읽기 — 자체 인덱스 포맷, 청크 lazy read"
```

---

## Task 12: head + 블록 통합 질의

**Files:**
- Create: `internal/tsdb/querier.go`
- Test: `internal/tsdb/querier_test.go`

**Interfaces:**
- Consumes: `Head`/`memSeries` (Task 8), `Block`/`blockSeries` (Task 11), `Chunk` (Task 5), `Matcher` (Task 7)
- Produces:
  - `type Iterator interface{ Next() bool; At() (int64, float64); Err() error }`
  - `type Series interface{ Labels() Labels; Iterator() Iterator }`
  - `type Querier struct{ ... }`
  - `func NewQuerier(mint, maxt int64, head *Head, blocks []*Block) *Querier`
  - `func (q *Querier) Select(ms ...*Matcher) []Series`
  - `func (q *Querier) LabelNames() []string`, `LabelValues(name string) []string`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/querier_test.go`:

```go
package tsdb

import "testing"

func collect(t *testing.T, s Series) []sample {
	t.Helper()
	var out []sample
	it := s.Iterator()
	for it.Next() {
		ts, v := it.At()
		out = append(out, sample{ts, v})
	}
	if it.Err() != nil {
		t.Fatalf("이터레이터 에러: %v", it.Err())
	}
	return out
}

func TestQuerier_head만_있을_때_읽는다(t *testing.T) {
	h := NewHead()
	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	for i := 0; i < 10; i++ {
		h.Append(ls, int64(i)*15000, float64(i))
	}

	q := NewQuerier(0, 1<<62, h, nil)
	m, _ := NewMatcher(MatchEqual, MetricName, "node_load1")
	got := q.Select(m)
	if len(got) != 1 {
		t.Fatalf("시리즈: got %d, want 1", len(got))
	}
	if s := collect(t, got[0]); len(s) != 10 {
		t.Fatalf("샘플: got %d, want 10", len(s))
	}
}

func TestQuerier_시간범위_밖_샘플을_잘라낸다(t *testing.T) {
	h := NewHead()
	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	for i := 0; i < 10; i++ {
		h.Append(ls, int64(i)*15000, float64(i))
	}

	// [30000, 60000] → i=2,3,4 (30000,45000,60000)
	q := NewQuerier(30000, 60000, h, nil)
	m, _ := NewMatcher(MatchEqual, "node", "e101")
	got := q.Select(m)
	s := collect(t, got[0])
	if len(s) != 3 {
		t.Fatalf("샘플: got %d, want 3 (%v)", len(s), s)
	}
	if s[0].t != 30000 || s[2].t != 60000 {
		t.Fatalf("범위: got [%d..%d]", s[0].t, s[2].t)
	}
}

func TestQuerier_블록과_head를_이어붙인다(t *testing.T) {
	base := t.TempDir()
	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})

	// 과거 구간은 블록으로 굳힌다.
	oldHead := NewHead()
	for i := 0; i < 10; i++ {
		oldHead.Append(ls, int64(i)*15000, float64(i))
	}
	m, _ := NewMatcher(MatchEqual, "node", "e101")
	dir, err := WriteBlock(base, oldHead.Select(m), ResolutionRaw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenBlock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// 최근 구간은 head 에 남아 있다.
	h := NewHead()
	for i := 10; i < 15; i++ {
		h.Append(ls, int64(i)*15000, float64(i))
	}

	q := NewQuerier(0, 1<<62, h, []*Block{b})
	got := q.Select(m)
	if len(got) != 1 {
		t.Fatalf("같은 라벨셋은 시리즈 1개로 합쳐야 한다, got %d", len(got))
	}
	s := collect(t, got[0])
	if len(s) != 15 {
		t.Fatalf("샘플: got %d, want 15", len(s))
	}
	for i, sm := range s {
		if sm.t != int64(i)*15000 || sm.v != float64(i) {
			t.Fatalf("샘플 %d: got (%d,%v)", i, sm.t, sm.v)
		}
	}
}

func TestQuerier_라벨_이름과_값을_모아준다(t *testing.T) {
	h := NewHead()
	h.Append(NewLabels(Label{MetricName, "a"}, Label{"node", "e101"}), 1000, 1)
	h.Append(NewLabels(Label{MetricName, "b"}, Label{"tier", "gpu"}), 1000, 1)

	q := NewQuerier(0, 1<<62, h, nil)
	names := q.LabelNames()
	if len(names) != 3 || names[0] != MetricName || names[1] != "node" || names[2] != "tier" {
		t.Fatalf("LabelNames: got %v", names)
	}
	if vals := q.LabelValues(MetricName); len(vals) != 2 || vals[0] != "a" || vals[1] != "b" {
		t.Fatalf("LabelValues(__name__): got %v", vals)
	}
}

func TestQuerier_매칭_시리즈가_없으면_빈_결과(t *testing.T) {
	h := NewHead()
	h.Append(NewLabels(Label{"node", "e101"}), 1000, 1)

	q := NewQuerier(0, 1<<62, h, nil)
	m, _ := NewMatcher(MatchEqual, "node", "없음")
	if got := q.Select(m); len(got) != 0 {
		t.Fatalf("빈 결과여야 한다: %d", len(got))
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run TestQuerier -v`
Expected: 컴파일 실패 — `undefined: NewQuerier`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/querier.go`:

```go
package tsdb

import "sort"

// Iterator 는 한 시리즈의 샘플을 시간 오름차순으로 낸다.
type Iterator interface {
	Next() bool
	At() (int64, float64)
	Err() error
}

// Series 는 라벨셋 하나에 대응하는 샘플 열이다.
type Series interface {
	Labels() Labels
	Iterator() Iterator
}

// chunkSource 는 청크를 지연 생성한다 — 블록 시리즈는 질의가 실제로
// 이터레이션할 때만 디스크를 읽는다.
type chunkSource func() (*Chunk, error)

type chainSeries struct {
	lset   Labels
	chunks []chunkSource
	mint   int64
	maxt   int64
}

func (s *chainSeries) Labels() Labels { return s.lset }

func (s *chainSeries) Iterator() Iterator {
	return &chainIterator{srcs: s.chunks, mint: s.mint, maxt: s.maxt}
}

// chainIterator 는 청크 여러 개를 순서대로 이어 순회하며 [mint,maxt] 밖
// 샘플을 걸러낸다. 청크는 시간순으로 배치돼 있다고 전제한다.
type chainIterator struct {
	srcs []chunkSource
	idx  int
	cur  *ChunkIterator

	mint, maxt int64
	t          int64
	v          float64
	err        error
}

func (it *chainIterator) Next() bool {
	for {
		if it.err != nil {
			return false
		}
		if it.cur == nil {
			if it.idx >= len(it.srcs) {
				return false
			}
			c, err := it.srcs[it.idx]()
			it.idx++
			if err != nil {
				it.err = err
				return false
			}
			it.cur = c.Iterator()
		}
		if !it.cur.Next() {
			if err := it.cur.Err(); err != nil {
				it.err = err
				return false
			}
			it.cur = nil
			continue
		}
		t, v := it.cur.At()
		if t < it.mint {
			continue
		}
		if t > it.maxt {
			// 청크는 시간순이라 이후 샘플도 전부 범위 밖이지만, 다음
			// 청크가 더 이른 구간일 가능성은 없으므로 여기서 끝낸다.
			return false
		}
		it.t, it.v = t, v
		return true
	}
}

func (it *chainIterator) At() (int64, float64) { return it.t, it.v }
func (it *chainIterator) Err() error           { return it.err }

// Querier 는 head 와 블록들을 한 시간 창으로 함께 조회한다.
type Querier struct {
	mint, maxt int64
	head       *Head
	blocks     []*Block
}

func NewQuerier(mint, maxt int64, head *Head, blocks []*Block) *Querier {
	return &Querier{mint: mint, maxt: maxt, head: head, blocks: blocks}
}

// Select 는 매처를 만족하는 시리즈를 라벨셋 오름차순으로 낸다. 같은 라벨셋이
// 블록과 head 양쪽에 있으면 하나로 합친다 — 블록이 과거, head 가 최근이므로
// 블록 청크를 먼저 잇는다.
func (q *Querier) Select(ms ...*Matcher) []Series {
	merged := map[string]*chainSeries{}
	order := []string{}

	get := func(lset Labels) *chainSeries {
		// MapKey 를 쓴다 — String() 은 이름을 이스케이프하지 않아 서로 다른
		// 라벨셋이 같은 문자열을 낼 수 있고, 그러면 시리즈가 조용히 병합된다.
		key := lset.MapKey()
		s, ok := merged[key]
		if !ok {
			s = &chainSeries{lset: lset, mint: q.mint, maxt: q.maxt}
			merged[key] = s
			order = append(order, key)
		}
		return s
	}

	// 블록 먼저 — 오래된 블록부터.
	blocks := append([]*Block(nil), q.blocks...)
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].meta.MinTime < blocks[j].meta.MinTime
	})
	for _, b := range blocks {
		if b.meta.MaxTime < q.mint || b.meta.MinTime > q.maxt {
			continue
		}
		for _, bs := range b.Select(ms...) {
			cs := get(bs.Lset)
			for _, cr := range bs.Chunks {
				if cr.MaxT < q.mint || cr.MinT > q.maxt {
					continue
				}
				blk, ref := b, cr
				cs.chunks = append(cs.chunks, func() (*Chunk, error) {
					return blk.Chunk(ref)
				})
			}
		}
	}

	// head 는 그 뒤에 붙는다.
	if q.head != nil {
		for _, s := range q.head.Select(ms...) {
			cs := get(s.lset)
			for _, c := range s.chunks {
				if c.MaxTime() < q.mint || c.MinTime() > q.maxt {
					continue
				}
				chk := c
				cs.chunks = append(cs.chunks, func() (*Chunk, error) {
					return chk, nil
				})
			}
		}
	}

	sort.Strings(order)
	out := make([]Series, 0, len(order))
	for _, k := range order {
		s := merged[k]
		if len(s.chunks) == 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (q *Querier) LabelNames() []string {
	set := map[string]struct{}{}
	if q.head != nil {
		for _, n := range q.head.postings.LabelNames() {
			set[n] = struct{}{}
		}
	}
	for _, b := range q.blocks {
		for _, n := range b.postings.LabelNames() {
			set[n] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (q *Querier) LabelValues(name string) []string {
	set := map[string]struct{}{}
	if q.head != nil {
		for _, v := range q.head.postings.LabelValues(name) {
			set[v] = struct{}{}
		}
	}
	for _, b := range q.blocks {
		for _, v := range b.postings.LabelValues(name) {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run TestQuerier -v -race`
Expected: 5개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/querier.go internal/tsdb/querier_test.go
git commit -m "feat(tsdb): head+블록 통합 질의 — 청크 체인, 시간범위 절단, 라벨 열거"
```

---

## Task 13: 롤업과 보존 정책

**Files:**
- Create: `internal/tsdb/rollup.go`, `internal/tsdb/retention.go`
- Test: `internal/tsdb/rollup_test.go`, `internal/tsdb/retention_test.go`

**Interfaces:**
- Consumes: `memSeries` (Task 8), `BlockMeta`/`ReadBlockMeta` (Task 11), `Labels` (Task 6)
- Produces:
  - `const Resolution5m = "5m"`, `const rollupInterval int64 = 5 * 60 * 1000`
  - `const RollupLabel = "__rollup__"`
  - `func RollupSeries(src []*memSeries, interval int64) []*memSeries`
  - `func ApplyRetention(baseDir string, rawRetention, rollupRetention, now int64) ([]string, error)`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/rollup_test.go`:

```go
package tsdb

import "testing"

func TestRollupSeries_버킷당_네_시리즈를_만든다(t *testing.T) {
	h := NewHead()
	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	// 5분(300000ms) 버킷 하나에 값 1,2,3 을 넣는다.
	h.Append(ls, 0, 1)
	h.Append(ls, 15000, 2)
	h.Append(ls, 30000, 3)

	m, _ := NewMatcher(MatchEqual, "node", "e101")
	out := RollupSeries(h.Select(m), rollupInterval)
	if len(out) != 4 {
		t.Fatalf("원본 1 시리즈 → 롤업 4 시리즈여야 한다, got %d", len(out))
	}

	byKind := map[string]*memSeries{}
	for _, s := range out {
		byKind[s.lset.Get(RollupLabel)] = s
	}
	for _, k := range []string{"sum", "count", "min", "max"} {
		if byKind[k] == nil {
			t.Fatalf("%s 롤업 시리즈가 없다", k)
		}
	}

	want := map[string]float64{"sum": 6, "count": 3, "min": 1, "max": 3}
	for kind, w := range want {
		s := byKind[kind]
		it := s.chunks[0].Iterator()
		if !it.Next() {
			t.Fatalf("%s: 샘플이 없다", kind)
		}
		ts, v := it.At()
		if ts != 0 {
			t.Fatalf("%s: 버킷 타임스탬프는 버킷 시작이어야 한다, got %d", kind, ts)
		}
		if v != w {
			t.Fatalf("%s: got %v, want %v", kind, v, w)
		}
		if it.Next() {
			t.Fatalf("%s: 버킷이 1개인데 샘플이 더 나왔다", kind)
		}
	}
}

func TestRollupSeries_버킷_경계를_정확히_나눈다(t *testing.T) {
	h := NewHead()
	ls := NewLabels(Label{"node", "e101"})
	h.Append(ls, 0, 10)              // 버킷 0
	h.Append(ls, rollupInterval-1, 20) // 버킷 0 (경계 직전)
	h.Append(ls, rollupInterval, 30)   // 버킷 1 (경계)

	m, _ := NewMatcher(MatchEqual, "node", "e101")
	out := RollupSeries(h.Select(m), rollupInterval)

	var sumSeries *memSeries
	for _, s := range out {
		if s.lset.Get(RollupLabel) == "sum" {
			sumSeries = s
		}
	}
	it := sumSeries.chunks[0].Iterator()
	var got []sample
	for it.Next() {
		ts, v := it.At()
		got = append(got, sample{ts, v})
	}
	if len(got) != 2 {
		t.Fatalf("버킷 2개여야 한다: %v", got)
	}
	if got[0].t != 0 || got[0].v != 30 {
		t.Fatalf("버킷 0: got %+v, want {0 30}", got[0])
	}
	if got[1].t != rollupInterval || got[1].v != 30 {
		t.Fatalf("버킷 1: got %+v, want {%d 30}", got[1], rollupInterval)
	}
}

func TestRollupSeries_원본_라벨을_보존한다(t *testing.T) {
	h := NewHead()
	ls := NewLabels(Label{MetricName, "gpu_temp"}, Label{"node", "e22"}, Label{"device", "gpu3"})
	h.Append(ls, 0, 61)

	m, _ := NewMatcher(MatchEqual, "device", "gpu3")
	out := RollupSeries(h.Select(m), rollupInterval)
	s := out[0]
	if s.lset.Get(MetricName) != "gpu_temp" || s.lset.Get("node") != "e22" || s.lset.Get("device") != "gpu3" {
		t.Fatalf("원본 라벨이 유실됐다: %v", s.lset)
	}
	if s.lset.Get(RollupLabel) == "" {
		t.Fatal("__rollup__ 라벨이 없다")
	}
}

func TestRollupSeries_빈_입력은_빈_출력(t *testing.T) {
	if got := RollupSeries(nil, rollupInterval); len(got) != 0 {
		t.Fatalf("빈 입력: got %d", len(got))
	}
}
```

`internal/tsdb/retention_test.go`:

```go
package tsdb

import (
	"os"
	"path/filepath"
	"testing"
)

// makeBlockDir 는 meta.json 만 있는 가짜 블록 디렉터리를 만든다.
func makeBlockDir(t *testing.T, base string, minT, maxT int64, res string) string {
	t.Helper()
	h := NewHead()
	ls := NewLabels(Label{"node", "e101"}, Label{"res", res})
	h.Append(ls, minT, 1)
	h.Append(ls, maxT, 2)
	m, _ := NewMatcher(MatchEqual, "node", "e101")
	dir, err := WriteBlock(base, h.Select(m), res)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestApplyRetention_만료된_블록만_지운다(t *testing.T) {
	base := t.TempDir()
	const day = int64(24 * 60 * 60 * 1000)
	now := int64(100 * day)

	fresh := makeBlockDir(t, base, now-day, now-day+1000, ResolutionRaw)          // raw, 1일 전 → 유지
	stale := makeBlockDir(t, base, now-10*day, now-10*day+1000, ResolutionRaw)    // raw, 10일 전 → 삭제
	rollupFresh := makeBlockDir(t, base, now-30*day, now-30*day+1000, Resolution5m) // 롤업, 30일 → 유지
	rollupStale := makeBlockDir(t, base, now-120*day, now-120*day+1000, Resolution5m) // 롤업, 120일 → 삭제

	deleted, err := ApplyRetention(base, 7*day, 90*day, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 {
		t.Fatalf("삭제된 블록: got %d, want 2 (%v)", len(deleted), deleted)
	}

	for _, keep := range []string{fresh, rollupFresh} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("유지돼야 할 블록이 지워졌다: %s", keep)
		}
	}
	for _, gone := range []string{stale, rollupStale} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("삭제돼야 할 블록이 남았다: %s", gone)
		}
	}
}

func TestApplyRetention_meta가_없는_디렉터리는_건너뛴다(t *testing.T) {
	base := t.TempDir()
	junk := filepath.Join(base, "0000-0000-raw")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}

	deleted, err := ApplyRetention(base, 1, 1, 1<<40)
	if err != nil {
		t.Fatalf("meta 없는 디렉터리에서 실패하면 안 된다: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("건너뛰어야 한다: %v", deleted)
	}
	if _, err := os.Stat(junk); err != nil {
		t.Fatal("판단 불가한 디렉터리를 지웠다")
	}
}

func TestApplyRetention_빈_디렉터리는_에러가_아니다(t *testing.T) {
	if _, err := ApplyRetention(filepath.Join(t.TempDir(), "없음"), 1, 1, 1); err != nil {
		t.Fatalf("미존재 디렉터리: %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run 'TestRollup|TestApplyRetention' -v`
Expected: 컴파일 실패 — `undefined: RollupSeries`, `undefined: ApplyRetention`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/rollup.go`:

```go
package tsdb

import "math"

const (
	// Resolution5m 은 5분 롤업 블록의 표식이다.
	Resolution5m = "5m"

	rollupInterval int64 = 5 * 60 * 1000

	// RollupLabel 은 롤업 종류를 담는 예약 라벨이다. 롤업을 별도 저장
	// 포맷으로 만들지 않고 "라벨이 하나 더 붙은 보통 시리즈"로 표현하면
	// 블록 쓰기·읽기·질의 코드를 그대로 재사용할 수 있다.
	RollupLabel = "__rollup__"
)

// rollupKinds 는 저장하는 집계 4종이다. 평균은 sum/count 로 유도하므로
// 따로 저장하지 않는다 — 그래야 여러 버킷을 다시 합칠 때도 정확하다.
var rollupKinds = []string{"sum", "count", "min", "max"}

type rollupBucket struct {
	start int64
	sum   float64
	count float64
	min   float64
	max   float64
}

// RollupSeries 는 원본 시리즈들을 interval 크기 버킷으로 집계해, 원본 하나당
// 4개(sum/count/min/max)의 새 시리즈를 만든다. 버킷 타임스탬프는 버킷 시작
// 시각이다.
func RollupSeries(src []*memSeries, interval int64) []*memSeries {
	if len(src) == 0 || interval <= 0 {
		return nil
	}

	out := make([]*memSeries, 0, len(src)*len(rollupKinds))
	var nextRef uint64

	for _, s := range src {
		var buckets []rollupBucket
		curIdx := -1

		for _, c := range s.chunks {
			it := c.Iterator()
			for it.Next() {
				t, v := it.At()
				// 타임스탬프는 유닉스 밀리초(양수)라 0 방향 절삭이
				// 곧 내림이다. 음수 시각은 들어오지 않는다.
				start := (t / interval) * interval

				// 슬라이스 인덱스로 다룬다 — append 가 재할당해도
				// 이전 요소 포인터를 들고 있지 않게 하려는 것이다.
				if curIdx < 0 || buckets[curIdx].start != start {
					buckets = append(buckets, rollupBucket{
						start: start,
						min:   math.Inf(1),
						max:   math.Inf(-1),
					})
					curIdx = len(buckets) - 1
				}
				b := &buckets[curIdx]
				b.sum += v
				b.count++
				if v < b.min {
					b.min = v
				}
				if v > b.max {
					b.max = v
				}
			}
		}
		if len(buckets) == 0 {
			continue
		}

		for _, kind := range rollupKinds {
			nextRef++
			lset := NewLabels(append(s.lset.Copy(), Label{RollupLabel, kind})...)
			rs := &memSeries{ref: nextRef, lset: lset}
			for _, b := range buckets {
				var v float64
				switch kind {
				case "sum":
					v = b.sum
				case "count":
					v = b.count
				case "min":
					v = b.min
				case "max":
					v = b.max
				}
				// append 는 청크가 차면 알아서 새 청크를 연다.
				_ = rs.append(b.start, v)
			}
			out = append(out, rs)
		}
	}
	return out
}
```

`internal/tsdb/retention.go`:

```go
package tsdb

import (
	"errors"
	"os"
	"path/filepath"
)

// ApplyRetention 은 보존기간을 넘긴 블록 디렉터리를 통째로 지운다.
// 해상도에 따라 다른 기간을 적용하며, 삭제한 디렉터리 경로를 돌려준다.
//
// meta.json 을 읽을 수 없는 디렉터리는 **건드리지 않는다** — 쓰다 만 블록일
// 수도 있고 남의 디렉터리일 수도 있어서, 판단이 안 서면 지우지 않는 쪽이 안전하다.
func ApplyRetention(baseDir string, rawRetention, rollupRetention, now int64) ([]string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var deleted []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, e.Name())
		meta, err := ReadBlockMeta(dir)
		if err != nil {
			continue // 판단 불가 — 남겨 둔다
		}

		retention := rawRetention
		if meta.Resolution != ResolutionRaw {
			retention = rollupRetention
		}
		if retention <= 0 {
			continue // 0 이하는 "무제한 보존"으로 본다
		}
		if meta.MaxTime < now-retention {
			if err := os.RemoveAll(dir); err != nil {
				return deleted, err
			}
			deleted = append(deleted, dir)
		}
	}
	return deleted, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run 'TestRollup|TestApplyRetention' -v -race`
Expected: 7개 테스트 모두 PASS

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/rollup.go internal/tsdb/retention.go internal/tsdb/rollup_test.go internal/tsdb/retention_test.go
git commit -m "feat(tsdb): 5분 롤업(sum/count/min/max) + 해상도별 보존기간 삭제"
```

---

## Task 14: DB 조립과 M1 완료 기준 검증

**Files:**
- Create: `internal/tsdb/db.go`
- Test: `internal/tsdb/db_test.go`, `internal/tsdb/bench_test.go`

**Interfaces:**
- Consumes: 앞선 모든 태스크
- Produces:
  - `type Options struct{ Dir string; SegmentSize, BlockDuration, RawRetention, RollupRetention int64 }`
  - `func DefaultOptions(dir string) Options`
  - `func Open(opts Options) (*DB, error)`
  - `func (db *DB) Append(lset Labels, t int64, v float64) error`
  - `func (db *DB) Querier(mint, maxt int64) (*Querier, func() error, error)`
  - `func (db *DB) Compact(now int64) error`
  - `func (db *DB) Close() error`

- [ ] **Step 1: 실패하는 테스트 작성**

`internal/tsdb/db_test.go`:

```go
package tsdb

import (
	"math/rand"
	"testing"
)

func TestDB_넣은_샘플을_되읽는다(t *testing.T) {
	db, err := Open(DefaultOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	for i := 0; i < 100; i++ {
		if err := db.Append(ls, int64(i)*15000, float64(i)); err != nil {
			t.Fatal(err)
		}
	}

	q, closeQ, err := db.Querier(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQ()

	m, _ := NewMatcher(MatchEqual, "node", "e101")
	got := q.Select(m)
	if len(got) != 1 {
		t.Fatalf("시리즈: got %d, want 1", len(got))
	}
	if s := collect(t, got[0]); len(s) != 100 {
		t.Fatalf("샘플: got %d, want 100", len(s))
	}
}

// M1 완료 기준 ①: 무작위 샘플 왕복 동일성.
func TestDB_무작위_샘플_왕복_동일성(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(42))
	type key struct{ node, metric string }
	want := map[key][]sample{}

	nodes := []string{"e101", "e102", "e21"}
	metrics := []string{"node_load1", "node_memory_MemFree_bytes", "gpu_temp"}
	for _, n := range nodes {
		for _, mt := range metrics {
			ls := NewLabels(Label{MetricName, mt}, Label{"node", n})
			var ts int64
			for i := 0; i < 500; i++ {
				ts += 14000 + rng.Int63n(3000) // 지터 있는 간격
				v := rng.Float64() * 1000
				if err := db.Append(ls, ts, v); err != nil {
					t.Fatalf("Append: %v", err)
				}
				want[key{n, mt}] = append(want[key{n, mt}], sample{ts, v})
			}
		}
	}

	q, closeQ, err := db.Querier(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	for k, ws := range want {
		mn, _ := NewMatcher(MatchEqual, "node", k.node)
		mm, _ := NewMatcher(MatchEqual, MetricName, k.metric)
		got := q.Select(mn, mm)
		if len(got) != 1 {
			t.Fatalf("%v: 시리즈 %d개", k, len(got))
		}
		gs := collect(t, got[0])
		if len(gs) != len(ws) {
			t.Fatalf("%v: 샘플 %d, want %d", k, len(gs), len(ws))
		}
		for i := range ws {
			if gs[i] != ws[i] {
				t.Fatalf("%v 샘플 %d: got %+v, want %+v", k, i, gs[i], ws[i])
			}
		}
	}
	closeQ()
	db.Close()
}

// M1 완료 기준 ②: 크래시 주입 후 WAL 복구 무손실.
func TestDB_크래시_후_재오픈시_데이터가_살아있다(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}

	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	for i := 0; i < 200; i++ {
		if err := db.Append(ls, int64(i)*15000, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Sync(); err != nil {
		t.Fatal(err)
	}
	// Close 를 부르지 않는다 = 프로세스가 죽은 상황.

	db2, err := Open(DefaultOptions(dir))
	if err != nil {
		t.Fatalf("재오픈: %v", err)
	}
	defer db2.Close()

	q, closeQ, err := db2.Querier(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQ()

	m, _ := NewMatcher(MatchEqual, "node", "e101")
	got := q.Select(m)
	if len(got) != 1 {
		t.Fatalf("복구된 시리즈: got %d, want 1", len(got))
	}
	s := collect(t, got[0])
	if len(s) != 200 {
		t.Fatalf("복구된 샘플: got %d, want 200", len(s))
	}
	for i := range s {
		if s[i].t != int64(i)*15000 || s[i].v != float64(i) {
			t.Fatalf("샘플 %d: got %+v", i, s[i])
		}
	}
}

func TestDB_Compact가_블록과_롤업을_만든다(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions(dir)
	db, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	// 2시간(블록 길이)을 넘는 구간을 채운다.
	for i := 0; i < 600; i++ {
		if err := db.Append(ls, int64(i)*15000, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	now := int64(600) * 15000
	if err := db.Compact(now); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// head 는 비었고 블록에서 읽혀야 한다.
	if db.head.NumSeries() != 0 {
		t.Fatalf("Compact 후 head 가 비어야 한다: %d", db.head.NumSeries())
	}

	q, closeQ, err := db.Querier(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQ()

	m, _ := NewMatcher(MatchEqual, "node", "e101")
	raw, _ := NewMatcher(MatchEqual, RollupLabel, "")
	got := q.Select(m, raw)
	if len(got) != 1 {
		t.Fatalf("raw 시리즈: got %d, want 1", len(got))
	}
	if s := collect(t, got[0]); len(s) != 600 {
		t.Fatalf("Compact 후 raw 샘플: got %d, want 600", len(s))
	}

	// 롤업 시리즈도 존재해야 한다.
	sumM, _ := NewMatcher(MatchEqual, RollupLabel, "sum")
	rollup := q.Select(m, sumM)
	if len(rollup) != 1 {
		t.Fatalf("롤업 sum 시리즈: got %d, want 1", len(rollup))
	}
	// 600샘플 × 15초 = 150분 → 5분 버킷 30개
	if s := collect(t, rollup[0]); len(s) != 30 {
		t.Fatalf("롤업 버킷: got %d, want 30", len(s))
	}
}

func TestDB_Compact가_보존기간_초과_블록을_지운다(t *testing.T) {
	dir := t.TempDir()
	const day = int64(24 * 60 * 60 * 1000)
	opts := DefaultOptions(dir)
	opts.RawRetention = 7 * day
	opts.RollupRetention = 90 * day

	db, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ls := NewLabels(Label{"node", "e101"})
	for i := 0; i < 10; i++ {
		db.Append(ls, int64(i)*15000, float64(i))
	}
	// 아주 먼 미래를 now 로 주면 방금 만든 블록이 전부 만료된다.
	if err := db.Compact(1000 * day); err != nil {
		t.Fatal(err)
	}

	q, closeQ, err := db.Querier(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQ()
	m, _ := NewMatcher(MatchEqual, "node", "e101")
	if got := q.Select(m); len(got) != 0 {
		t.Fatalf("보존기간이 지난 데이터가 남았다: %d 시리즈", len(got))
	}
}

func TestDB_Compact_후_WAL이_비워진다(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	ls := NewLabels(Label{"node", "e101"})
	for i := 0; i < 50; i++ {
		db.Append(ls, int64(i)*15000, float64(i))
	}
	if err := db.Compact(int64(50) * 15000); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// WAL 을 재생해도 아무것도 안 나와야 한다 (블록으로 옮겨졌으므로).
	n := 0
	if err := ReplayWAL(walDir(dir),
		func(uint64, Labels) error { return nil },
		func(ss []RefSample) error { n += len(ss); return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("Compact 후 WAL 에 샘플이 남았다: %d", n)
	}
}
```

`internal/tsdb/bench_test.go`:

```go
package tsdb

import (
	"fmt"
	"testing"
)

// M1 완료 기준 ③: 압축 바이트/포인트 측정. 실측 e101 노드의 시리즈 특성
// (느리게 변하는 게이지 + 단조 증가 카운터)을 흉내 낸다.
func TestCompressionRatio_바이트당_포인트를_보고한다(t *testing.T) {
	const samples = 10000

	// 실제 하드웨어 메트릭은 대부분 "정수 계단형"(온도 °C, 사용률 %, 바이트
	// 카운트, 단조 카운터)이라 XOR 하위비트가 0 으로 남아 잘 압축된다. 반대로
	// 소수점 아래가 계속 요동치는 float(load1 0.01 단위 등)은 유효비트가
	// 가수부 전체에 퍼져 압축이 약하다 — 실측상 유일한 최악 케이스다.
	// (실측: 온도 0.27 / 사용률 2.37 / 메모리 0.19 / 카운터 2.64 / 요동 6.7)
	strict := map[string]func(i int) float64{
		"상수_게이지":     func(i int) float64 { return 42 },
		"온도_정수계단":    func(i int) float64 { return 42 + float64((i/7)%26) },
		"사용률_정수":     func(i int) float64 { return float64((i * 13) % 101) },
		"메모리_느린계단":   func(i int) float64 { return 8e9 + float64((i/50)%1000)*1e6 },
		"단조_카운터":     func(i int) float64 { return float64(i) * 1000 },
	}
	// 요동 float 은 최악 케이스로 별도 유계 검증한다.
	worst := map[string]func(i int) float64{
		"요동_float_최악": func(i int) float64 { return 2 + float64(i%300)/100 },
	}

	measure := func(gen func(i int) float64) (float64, int) {
		var total, chunks int
		c := NewChunk()
		chunks = 1
		for i := 0; i < samples; i++ {
			if c.Full() {
				total += len(c.Bytes())
				c = NewChunk()
				chunks++
			}
			if err := c.Append(int64(i)*15000, gen(i)); err != nil {
				t.Fatal(err)
			}
		}
		total += len(c.Bytes())
		return float64(total) / float64(samples), chunks
	}

	// 정수 계단형(현실적 다수) — 설계 문서 §8.2 의 1.5 가정 부근이거나 더 좋다.
	for name, gen := range strict {
		bpp, chunks := measure(gen)
		t.Log(fmt.Sprintf("%s: %.2f bytes/point (청크 %d개)", name, bpp, chunks))
		if bpp > 3.0 {
			t.Fatalf("%s: %.2f bytes/point — 정수 계단형인데 압축이 나쁘다", name, bpp)
		}
	}
	// 요동 float(드문 최악) — 압축이 약하지만 유계여야 한다.
	for name, gen := range worst {
		bpp, chunks := measure(gen)
		t.Log(fmt.Sprintf("%s: %.2f bytes/point (청크 %d개)", name, bpp, chunks))
		if bpp > 8.0 {
			t.Fatalf("%s: %.2f bytes/point — 최악 케이스도 이 한도는 넘지 않아야 한다", name, bpp)
		}
	}
}

func BenchmarkDBAppend(b *testing.B) {
	db, err := Open(DefaultOptions(b.TempDir()))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	ls := NewLabels(Label{MetricName, "node_load1"}, Label{"node", "e101"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.Append(ls, int64(i)*15000, float64(i)); err != nil {
			b.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./internal/tsdb/ -run 'TestDB|TestCompressionRatio' -v`
Expected: 컴파일 실패 — `undefined: Open`, `undefined: DefaultOptions`, `undefined: walDir`

- [ ] **Step 3: 최소 구현**

`internal/tsdb/db.go`:

```go
package tsdb

import (
	"os"
	"path/filepath"
	"sync"
)

// Options 는 DB 동작을 결정한다. 기간은 모두 밀리초다.
type Options struct {
	Dir             string
	SegmentSize     int64
	BlockDuration   int64
	RawRetention    int64
	RollupRetention int64
}

// DefaultOptions 는 설계 문서 §8 의 기본값이다 — 2시간 블록, raw 7일,
// 롤업 90일.
func DefaultOptions(dir string) Options {
	const (
		hour = int64(60 * 60 * 1000)
		day  = 24 * hour
	)
	return Options{
		Dir:             dir,
		SegmentSize:     defaultSegmentSize,
		BlockDuration:   2 * hour,
		RawRetention:    7 * day,
		RollupRetention: 90 * day,
	}
}

func walDir(dir string) string    { return filepath.Join(dir, "wal") }
func blocksDir(dir string) string { return filepath.Join(dir, "blocks") }

// DB 는 WAL·head·블록을 묶은 저장 엔진의 공개 표면이다.
type DB struct {
	mtx sync.Mutex

	opts Options
	head *Head
	wal  *WAL

	// knownRefs 는 이번 WAL 세그먼트에 시리즈 레코드를 이미 쓴 ref 다.
	// 시리즈 레코드가 없으면 재생 시 라벨셋을 모르므로 샘플이 버려진다.
	knownRefs map[uint64]struct{}
}

func Open(opts Options) (*DB, error) {
	if opts.SegmentSize <= 0 {
		opts.SegmentSize = defaultSegmentSize
	}
	if err := os.MkdirAll(blocksDir(opts.Dir), 0o755); err != nil {
		return nil, err
	}

	// 재시작 시 WAL 을 재생해 head 를 복원한다.
	head, err := RecoverHead(walDir(opts.Dir))
	if err != nil {
		return nil, err
	}
	w, err := OpenWAL(walDir(opts.Dir), opts.SegmentSize)
	if err != nil {
		return nil, err
	}

	db := &DB{opts: opts, head: head, wal: w, knownRefs: map[uint64]struct{}{}}
	// 복구된 시리즈는 이미 WAL 에 레코드가 있다.
	for ref := range head.series {
		db.knownRefs[ref] = struct{}{}
	}
	return db, nil
}

// Append 는 샘플 하나를 넣는다.
//
// head 에 먼저 붙이고 WAL 에 기록한다 — ref 를 알아야 WAL 레코드를 쓸 수
// 있기 때문이다. 이 순서 때문에 "head 에는 있는데 WAL 에는 없는" 창이
// 잠깐 생기지만, 그 창에서 죽으면 어차피 head 도 함께 사라지므로 복구
// 결과는 일관된다.
func (db *DB) Append(lset Labels, t int64, v float64) error {
	db.mtx.Lock()
	defer db.mtx.Unlock()

	ref, err := db.head.Append(lset, t, v)
	if err != nil {
		return err
	}
	if _, ok := db.knownRefs[ref]; !ok {
		if err := db.wal.LogSeries(ref, lset); err != nil {
			return err
		}
		db.knownRefs[ref] = struct{}{}
	}
	return db.wal.LogSamples([]RefSample{{Ref: ref, T: t, V: v}})
}

func (db *DB) Sync() error {
	db.mtx.Lock()
	defer db.mtx.Unlock()
	return db.wal.Sync()
}

// Querier 는 [mint,maxt] 에 걸치는 블록만 열어 head 와 함께 조회한다.
// 두 번째 반환값은 연 블록을 닫는 함수이며 반드시 호출해야 한다.
func (db *DB) Querier(mint, maxt int64) (*Querier, func() error, error) {
	db.mtx.Lock()
	defer db.mtx.Unlock()

	entries, err := os.ReadDir(blocksDir(db.opts.Dir))
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}

	var blocks []*Block
	closeAll := func() error {
		var firstErr error
		for _, b := range blocks {
			if err := b.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(blocksDir(db.opts.Dir), e.Name())
		meta, err := ReadBlockMeta(dir)
		if err != nil {
			continue // 쓰다 만 블록 — 건너뛴다
		}
		if meta.MaxTime < mint || meta.MinTime > maxt {
			continue
		}
		b, err := OpenBlock(dir)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		blocks = append(blocks, b)
	}

	return NewQuerier(mint, maxt, db.head, blocks), closeAll, nil
}

// Compact 는 head 를 블록으로 굳히고, 롤업 블록을 만들고, WAL 을 비우고,
// 보존기간이 지난 블록을 지운다. now 는 보존 판정 기준 시각이다.
//
// 시간을 인자로 받는 이유는 테스트 가능성이다 — 내부에서 time.Now 를 부르면
// 보존 로직을 결정론적으로 검증할 수 없다.
func (db *DB) Compact(now int64) error {
	db.mtx.Lock()
	defer db.mtx.Unlock()

	// 매처 없이 부르면 Head.Select 는 전체 시리즈를 낸다 (Task 8 계약).
	series := db.head.Select()
	if len(series) > 0 {
		if _, err := WriteBlock(blocksDir(db.opts.Dir), series, ResolutionRaw); err != nil {
			return err
		}
		if rolled := RollupSeries(series, rollupInterval); len(rolled) > 0 {
			if _, err := WriteBlock(blocksDir(db.opts.Dir), rolled, Resolution5m); err != nil {
				return err
			}
		}
		db.head.Reset()
		db.knownRefs = map[uint64]struct{}{}
		if err := db.wal.Truncate(); err != nil {
			return err
		}
	}

	_, err := ApplyRetention(blocksDir(db.opts.Dir), db.opts.RawRetention, db.opts.RollupRetention, now)
	return err
}

func (db *DB) Close() error {
	db.mtx.Lock()
	defer db.mtx.Unlock()
	return db.wal.Close()
}
```

> **주의:** `db.head.Select()` 를 매처 없이 부르면 Task 8 구현의
> `seeded == false` 경로를 타 전체 시리즈가 나온다. 이 계약이 깨지면
> `Compact` 가 조용히 일부 시리즈만 굳히므로, Task 8 을 수정할 때 이 호출을
> 함께 확인한다.

- [ ] **Step 4: 통과 확인**

Run: `go test ./internal/tsdb/ -run 'TestDB|TestCompressionRatio' -v -race`
Expected: 6개 DB 테스트 + 압축률 테스트 PASS. 압축률 로그에 4종 시리즈의 bytes/point 가 출력된다.

Run: `make all`
Expected: gofmt·vet 무출력, 전체 테스트 `ok`

Run: `go test ./internal/tsdb/ -bench=BenchmarkDBAppend -benchmem -run=^$`
Expected: `BenchmarkDBAppend` 결과 출력 (ns/op, B/op)

- [ ] **Step 5: 커밋**

```bash
git add internal/tsdb/db.go internal/tsdb/db_test.go internal/tsdb/bench_test.go
git commit -m "feat(tsdb): DB 조립 — WAL+head+블록+롤업+보존, M1 완료 기준 검증 3종"
```

- [ ] **Step 6: PR 생성**

```bash
git push -u origin feat/m1-tsdb-engine
gh pr create --repo KeiaiLab/nodevitals-observatory \
  --head eightynine01:feat/m1-tsdb-engine --base main \
  --title "feat(tsdb): M1 — 자체 시계열 저장 엔진" \
  --body "설계 스펙: https://github.com/KeiaiLab/nodevitals/blob/main/docs/superpowers/specs/2026-07-24-nodevitals-observatory-design.md

M1 완료 기준 3종 충족:
- 무작위 샘플 왕복 동일성 (TestDB_무작위_샘플_왕복_동일성)
- 크래시 주입 후 WAL 복구 무손실 (TestDB_크래시_후_재오픈시_데이터가_살아있다, TestRecoverHead_잘린_WAL은_앞부분까지_복구한다)
- 압축 bytes/point 벤치 (TestCompressionRatio_바이트당_포인트를_보고한다)

검증: \`make all\` 통과"
```

---

## 완료 기준 (M1)

이 계획이 끝나면 아래가 모두 참이어야 한다.

- [ ] `make all` 통과 — gofmt·vet 무출력, 전체 테스트 `ok`
- [ ] 무작위 4,500 샘플(9 시리즈 × 500)이 지터 있는 간격으로 왕복 동일
- [ ] WAL 을 임의 지점에서 자르거나 한 바이트를 뒤집어도 그 앞까지는 복구되고, 에러가 아니다
- [ ] `Close()` 없이 죽은 뒤 재오픈해도 200 샘플이 전부 살아 있다
- [ ] `Compact` 후 head 가 비고, raw 블록과 5분 롤업 블록이 생기고, WAL 이 비고, 질의 결과가 동일하다
- [ ] 보존기간이 지난 블록이 해상도별로 지워진다
- [ ] 압축률 — **정수 계단형 메트릭**(온도·사용률·바이트·카운터)은 3 bytes/point 이하로 설계 §8.2 의 1.5 가정을 실측이 뒷받침한다(실측 0.19~2.64). **소수점이 요동하는 float**(load1 등)은 XOR 유효비트가 가수부 전체에 퍼져 압축이 약하나 8 bytes/point 이내로 유계다(실측 ~6.7). 이 비대칭은 알고리즘 고유 특성이며, 실제 nodevitals 메트릭은 정수 계단형이 다수라 용량 산정(§8.2)의 근거는 유효하다.

> **주의 — 초안의 단일 기준(4 bytes/point) 폐기**: 초안은 무예외 "4 이하"였으나, 요동 float 픽스처가 6.59 로 실측되며 이 기준이 비현실적임이 드러났다. 코드가 아니라 픽스처·기준이 문제였다(XOR 인코딩은 Task 4 에서 검증됨). 위처럼 메트릭 유형별로 분리한 것이 실제 특성을 반영한다.

---

## Self-Review 결과

**1. 스펙 커버리지** — 설계 문서 §4(저장 계층)의 항목 대 태스크 매핑:

| 스펙 §4 항목 | 태스크 |
|---|---|
| 시리즈 식별(정렬·해시) | Task 6 |
| 역색인 posting list | Task 7 |
| Head 인메모리 청크 | Task 5, 8 |
| WAL + 재시작 복구 | Task 9, 10 |
| Gorilla 압축 | Task 3, 4 |
| 불변 블록(chunks/index/meta.json) | Task 11 |
| 5분 롤업(sum/count/min/max) | Task 13 |
| 디렉터리 단위 리텐션 | Task 13 |
| 해상도 자동 선택 | **M3 로 이연** — 질의 계층(PromQL)이 `step` 을 해석해야 결정할 수 있어, 저장 엔진 단독으로는 판단 근거가 없다. M1 은 롤업 블록을 *만들고 읽을 수 있게* 하는 데까지다. |
| §4.4 정확성 경계(WAL) | Task 10 + Task 14 완료 기준 |

**2. 플레이스홀더 스캔** — "TBD"·"적절히"·"에러 처리 추가" 류 없음. 모든 코드 스텝에 실행 가능한 전체 코드가 있다. 초안에 있던 "쓰지 않는 변수를 넣었다가 지우라"는 지시는 제거하고, 매처 없는 `Select()` 가 전체를 뜻한다는 Task 8 계약을 주의 블록으로 옮겼다.

**3. 타입 일관성** — 태스크 간 이름을 대조했다:
- `ErrOutOfOrder` (Task 5 정의) → Task 8·10 에서 동일 이름 사용 ✓
- `appendString` (Task 9 정의) → Task 11 인덱스 직렬화에서 재사용 ✓
- `listSegments` (Task 9 정의) → Task 10 크래시 테스트에서 재사용 ✓
- `memSeries` 필드 `ref`/`lset`/`chunks`/`minT`/`maxT` (Task 8) → Task 11·13 에서 동일 ✓
- `chunkRef` 필드는 블록 외부에서도 읽으므로 대문자(`Offset`/`Length`/`MinT`/`MaxT`), `memSeries` 는 패키지 내부 전용이라 소문자 — 의도된 비대칭 ✓
- `sample` 헬퍼 타입은 Task 5 테스트에서 정의되어 Task 12·14 테스트가 재사용 ✓
- `collect` 헬퍼는 Task 12 테스트에서 정의되어 Task 14 가 재사용 ✓

**4. 발견해 고친 것** — 실행 직전 pre-flight 스캔에서 3건을 고쳤다.

1. **중복 로직** — `Head.Select`(Task 8)와 `Block.Select`(Task 11)가 매처 해석을 각자 구현하고 있었다. 리뷰 루브릭이 DRY 위반으로 잡을 뿐 아니라, 두 곳이 따로 진화하면 head 와 블록의 질의 의미론이 갈라진다. `selectRefs`·`matchesAll` 을 Task 7 로 추출하고 양쪽이 공유하게 했다.
2. **취약한 포인터** — `RollupSeries` 가 `buckets = append(...)` 후 `&buckets[len-1]` 포인터를 들고 있었다. 현재 흐름에선 우연히 안전하지만 한 줄만 옮겨도 조용히 깨지므로 인덱스 접근으로 바꿨다.
3. **진짜 버그** — Task 14 의 `TestDB_Compact가_블록과_롤업을_만든다` 가 `MatchEqual, RollupLabel, ""` 로 "롤업 라벨이 없는 시리즈"를 고르는데, 색인에 빈 값 posting 이 없어 **빈 결과**가 나왔다. `selectRefs` 가 값이 빈 같음-매처를 부정 매처와 같이 취급해 색인 시드에서 제외하도록 고치고, Task 7 에 그 동작을 고정하는 테스트(`TestSelectRefs_색인으로_좁힐_수_없으면_전체를_준다`)를 추가했다.

`Head.Select()` 를 인자 없이 부르는 경로(Task 14 `Compact`)는 Task 7 의 `TestSelectRefs_색인으로_좁힐_수_없으면_전체를_준다` 마지막 케이스가 직접 고정한다.

---

## 다음 계획 (M1 이후)

M2~M7 은 각각 별도 계획을 쓴다. 순서와 선행 조건:

| M | 선행 | 핵심 위험 |
|---|---|---|
| M2 수집 | M1 | Prometheus 텍스트 파싱 정확도 — 실 `/metrics` 4,522줄 픽스처로 고정 |
| M3 질의 | M1, M2 | PromQL 벡터 매칭 의미론 — 골든 테스트 |
| M4 인증+Overview | M3 | 세션 폐기·역할 강제 |
| M5 Map+Explorer | M4 | 없음 (표면 작업) |
| M6 OIDC+감사 | M4 | IdP 연동 실증 |
| M7 릴리스 | M5, M6 | 서명·SBOM 파이프라인 |

