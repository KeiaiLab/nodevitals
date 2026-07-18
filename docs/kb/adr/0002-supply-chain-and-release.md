# ADR-0002: 공급망 신뢰 + 릴리스 파이프라인

- 상태: Accepted
- 날짜: 2026-07-18
- 관련: 운영준비도 감사(`docs/production-readiness.md`) High gap "SBOM/서명/스캔 0/4", ADR-0001(arm64 OSS 예외)

## 맥락

운영준비도 감사가 공급망 신뢰 부재를 High gap 으로 지적했다: SBOM 없음, 서명 없음, 취약점 스캔 없음, provenance 없음(0/4). 차트가 참조하는 `ghcr.io/keiailab/nodevitals:0.1.0[-gpu]` 이미지는 아직 미발행이라 발행 파이프라인 자체가 없다.

동시에 두 제약이 있다:

1. **거버넌스 §2.3/RFC-0002 는 GitHub Actions 를 영구 금지**한다(트리거: org billing SPOF, 24h+ 머지 차단 사고). 예외는 3종(Pages / Dependabot / release-tag-only after ADR)뿐. 이 repo 는 공개 OSS(§2.8 GitHub canonical)지만 org 규칙의 GH Actions 금지는 광범위하다.
2. **라이브 레지스트리 publish 는 비가역 outward 작업**이다 — 잘못 발행한 이미지/서명은 되돌리기 어렵다.

## 결정

**릴리스·공급망 단계를 로컬 `make` 타깃으로 코드화**한다 — 이 repo 의 기존 게이트 패턴(전 게이트가 `make` 타깃, CI 없음)과 정합하고, GH Actions 금지를 우회한다. 유지보수자가 `v*` 태그 시점에 로컬에서 실행한다.

- **취약점 스캔 = trivy** (`make scan`). HIGH/CRITICAL 발견 시 `--exit-code 1` 로 릴리스 차단. 실측(2026-07-18): 정적 이미지 = debian 12.15 base 0 + gobinary 0 취약점(distroless + static + 최소의존 설계의 결과).
- **SBOM = trivy CycloneDX** (`make sbom` → `dist/sbom-<ver>.cdx.json`). 실측: 21 컴포넌트. (syft 미설치 — trivy 가 스캔·SBOM 겸용이라 단일 도구 유지, §Simplicity.)
- **서명 = cosign keyless, 반드시 digest 로** (`$(IMG)@sha256:...`, 가변 tag 아님). tag 는 서명 후 다른 digest 로 재지정될 수 있어 서명이 아티팩트에 묶이지 않는다(cosign 안티패턴). Fulcio/Rekor OIDC 키리스, 발행된 레지스트리 참조에만 가능하므로 push 후 단계. **자동 make 타깃 아님** — publish 런북(§결과) 수동 단계.
- **provenance/attestation** = `docker buildx build --sbom=true --provenance=true` 는 **docker-container 드라이버**를 요구한다(classic `docker` 드라이버는 attestation emit 불가 — 감사 확인). 릴리스 빌드는 `docker buildx create --driver docker-container --use` 후 수행. post-build trivy SBOM 은 드라이버 무관 fallback.
- **멀티아치**: 정적 이미지 = `linux/amd64,linux/arm64`(ADR-0001), gpu 이미지 = `linux/amd64` 전용(go-nvml cgo, arm64 GPU 후속).

## 결과

- **make 자동화 = 안전·검증가능한 부분만** — `scan`(trivy vuln, HIGH/CRITICAL exit 1) / `sbom`(CycloneDX → dist/) / `release-verify`(양 이미지 build → **scan 양쪽** → sbom, **publish 없음**). 지금 동작(trivy 설치). command-injection 방지 = VERSION semver 검증 + 변수 따옴표.
- **publish + 서명 = 유지보수자 런북** — 자동 push/sign make 타깃은 폐기했다(비가역 outward + 미묘한 순서·digest 요건이 fire-and-forget 자동화에 부적합, 보안리뷰 지적: fail-open-publish / mutable-tag-sign / registry-fanout / scan-bypass):
  1. `make release-verify` — 양 이미지(static+gpu) 스캔 clean + SBOM. **push 전 fail-closed**: 취약 이미지는 레지스트리에 절대 도달하지 않는다.
  2. `ghcr.io` 로그인 + canonical repo(`ghcr.io/keiailab/nodevitals`)만 대상 — 레지스트리 fanout 금지.
  3. `docker buildx build --platform linux/amd64,linux/arm64 -t $IMG:$VER --push .` (gpu: `--target gpu --platform linux/amd64`).
  4. digest 확보: `DIG=$(docker buildx imagetools inspect $IMG:$VER --format '{{.Manifest.Digest}}')`.
  5. **digest 로 서명**(가변 tag 금지): `cosign sign --yes $IMG@$DIG` (gpu 동일).
  6. 검증: `cosign verify $IMG@$DIG --certificate-identity ... --certificate-oidc-issuer ...`.
- 라이브 publish 는 유지보수자 명시 실행 — AI 자율 발행 금지(비가역 outward). CI 봇 아님 → §2.3 GH Actions 금지 무위반, RFC-0002 예외 ADR 불요.
- 후속: Renovate 로 FROM digest-pin 관리(감사 Medium), provenance attestation(buildx docker-container 드라이버), 릴리스 노트 자동화.
- 트레이드오프(정직): 로컬 릴리스는 CI 대비 재현성·감사추적이 약하다(유지보수자 워크스테이션 의존). 공개 OSS 신뢰가 커지면 release-tag-only GH Actions(RFC-0002 예외 3 + 별도 ADR)로 승격 검토.
