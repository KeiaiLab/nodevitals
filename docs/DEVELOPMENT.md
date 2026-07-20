# nodevitals 개발 워크플로우 (2계정 fork 모델)

nodevitals 는 **fork 기반 기여 흐름**으로 개발한다. 두 GitHub 계정이 서로 다른 역할을 맡는다.

## 1. 2계정 역할 모델

| 계정 | 역할 | 책임 |
|---|---|---|
| **eightynine01** | 기여자(contributor) | `KeiaiLab/nodevitals` 를 fork 해 기능 개발. feature 브랜치 → 자기 fork 로 push → PR 생성 |
| **KeiaiLab-PHIL** | 메인테이너(maintainer) | upstream canonical(`KeiaiLab/nodevitals`) 소유. PR 리뷰·승인, `main` 머지, 머지 시점 릴리스 pipeline 실행 |

- **canonical(진본) = `KeiaiLab/nodevitals`** — 발행·릴리스 SSOT. 기여자가 직접 push 하지 않는다.
- **fork = `eightynine01/nodevitals`** — 개발 진입점. 여기서만 브랜치를 만들고 push 한다.

## 2. 클론 셋업 (remote 2개)

fork 를 `origin`, canonical 을 `upstream` 으로 두는 표준 구성:

```bash
# eightynine01 가 KeiaiLab/nodevitals 를 fork 한 뒤:
git clone https://github.com/eightynine01/nodevitals.git
cd nodevitals
git remote add upstream https://github.com/KeiaiLab/nodevitals.git
git remote -v
#   origin    https://github.com/eightynine01/nodevitals.git   (fork, push 대상)
#   upstream  https://github.com/KeiaiLab/nodevitals.git        (canonical, PR 대상)
```

## 3. 개발 흐름 (feature → PR → 승인 → 머지)

```bash
git switch -c feat/<slug>                        # feature 브랜치 (upstream/main 기준)
# ... 구현 + 테스트 ...
make all                                          # 로컬 게이트 (§5) — push 전 필수
git push -u origin feat/<slug>                    # fork(origin)로 push
```

- push 후 GitHub 에서 **PR: `eightynine01:feat/<slug>` → `KeiaiLab:main`** 생성.
- **KeiaiLab-PHIL 이 리뷰·승인** → upstream `main` 으로 머지.
- fork 의 `main` 에 직접 커밋하지 않는다. 모든 변경은 feature 브랜치 + PR 경유.

## 4. upstream 동기화 (rebase)

작업 시작 전·PR 전에 canonical 최신을 흡수한다:

```bash
git fetch upstream
git rebase upstream/main                          # feature 브랜치 위에서 재정렬
git push --force-with-lease origin feat/<slug>    # 자기 fork 브랜치 한정 (--force-with-lease)
```

## 5. "pipeline" 의 실체 — 로컬 make 게이트 + 브랜치 보호

**nodevitals 는 GitHub Actions 가 없다** (`docs/kb/adr/0002-supply-chain-and-release.md`; keiailab RFC-0002 가 GH Actions 를 영구 금지). 따라서 원격 CI 파이프라인 대신 **머지 게이트 2개**로 품질을 보장한다.

**(a) 로컬 `make` 게이트** — push/머지 전 실행하며 fail-closed(실패 시 차단):

| 명령 | 검증 | 필요 도구 |
|---|---|---|
| `make all` | fmt · vet · test · build | go |
| `make chart-lint` | Helm 차트 스키마 검증 | helm + kubeconform |
| `make release-verify` | 이미지 취약점 scan + SBOM (static+gpu 양쪽) | docker + trivy |

- `make all` = 매 push 전 기본 게이트.
- `make chart-lint` = `deploy/chart/**` 변경 시.
- `make release-verify` = 릴리스(`v*` 태그) 직전 — HIGH/CRITICAL 발견 시 릴리스 차단. **이미지 publish·cosign 서명은 자동화가 아니다** — 메인테이너 수동 런북(ADR-0002, 비가역 outward 작업).

**(b) GitHub 브랜치 보호** — upstream `main` 은 **KeiaiLab-PHIL 리뷰 승인 필수**로 보호한다. 승인 없이는 머지 불가.

→ 이 (a) 로컬 게이트 + (b) 브랜치 보호 승인, 두 가지가 nodevitals 의 "pipeline" 이다. 둘 다 통과해야만 canonical `main` 에 착지한다.

## 6. 운영 제약 — 헤드리스/백그라운드 에이전트의 gh 인증

**알려진 제약**: `gh`(GitHub CLI)는 인증 토큰을 **macOS keychain** 에 저장한다. 세션에서 분리된 **백그라운드/헤드리스 에이전트 프로세스는 keychain 을 읽지 못해** `gh`·`git` 의 GitHub 인증이 실패한다. (반면 `glab` 은 토큰을 파일에 저장하므로 이 제약이 없다.)

bg-에이전트로 GitHub 작업(push·PR)을 할 때는 PAT 를 환경변수로 export 한다:

```bash
export GH_TOKEN=<PAT>          # gh 가 keychain 대신 이 env 를 사용
export GITHUB_TOKEN=<PAT>      # gh credential helper 경유 git https push 인증
```

또는 `gh auth login` 이 **파일 토큰 저장소**를 쓰도록 구성한다. 이렇게 하면 keychain 접근 없이도 `gh`·`git` 이 동작한다.
