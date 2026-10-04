# 단일 릴리스 워크플로와 선택적 빌드 계획

작성일: 2026-10-04. 완료: 2026-10-05 KST. 상태: 구현·최종 네이티브 CI·review-loop·0.2.4 배포·공개 baseline 반복 측정 완료.
단일 job graph, 호환 계약, 선택 계획과 캐시 경로를 구현했다. CLI 6개·독립 browser 5개·Linux full 2개와 gate가 실제 `publish=false` 실행에서 통과했다. [구현 검증 기록](evidence/release-pipeline-validation.json)과 [job별 시간](evidence/release-pipeline-timings.json)에 근거를 남겼다. 이후 승인된 배포 단계에서 같은 워크플로로 stable `0.2.4` 게시·Pages 전환·익명 공개 설치/업그레이드 검증까지 완료했다. [공개 배포와 반복 측정](evidence/release-pipeline-public-validation.json)은 구현 완료 당시의 기록과 분리해 보존한다.

최종 승인 lock으로 다음 CLI 버전의 실제 계획을 계산했을 때 browser/full build·check matrix와 OCI 전송 선택이 모두 0개였다. isolated Linux CLI 교체에서도 기존 browser 패키지·활성화 선택·Codex 설정·사용자 데이터가 유지됐다. 첫/warm 실행의 full 모듈 18개와 이미지 10개의 제품 바이트·digest·신뢰 근거가 일치했고 ARM64 full job은 634초에서 347초로 줄었다.

공개 `0.2.4` baseline으로 CLI-only 후보를 두 번 실행했다. 초기 대기를 제외한 실행은 7분 51초·6분 40초였고, 초기 대기는 각각 4초·3분 28초였다. 두 실행 모두 CLI 6개만 빌드·검증하고 기존 모듈·이미지 33개의 전체 lock 기록을 그대로 유지했다. 후보 `0.2.5`는 게시하지 않았다. 첫 전체 배포의 게시 시작부터 공개 확인 완료까지는 5분 23초였다. CLI-only 게시의 2분 목표는 아직 측정하지 않았으며, 공개 검증용 artifact 다운로드와 릴리스 파일 전송 비용이 다음 성능 개선 대상이다.

## 목표와 범위

배포는 `.github/workflows/release.yml` 하나에서 시작하고 끝낸다. 모든 작업은 이 파일의 job으로 구성한다. 별도 workflow 호출과 run ID 수동 입력을 없앤다. Python/셸 스크립트로 공통 로직을 유지하고 YAML에는 job 의존성·권한·matrix만 둔다.

변경된 구성요소와 그 의존 구성요소만 빌드한다. 변경 없는 패키지는 기존에 검증된 공개 결과물의 URL·바이트·digest·생산 근거를 그대로 재사용한다. 빌드 캐시는 컴파일과 이미지 레이어를 가속한다. 공개 결과물 재사용과 캐시 적중은 별도 조건이다.

지원 범위는 CLI 6개 네이티브 대상, 독립 브라우저 5개 대상, full Linux 2개 대상을 유지한다. 이번 개편은 브라우저 기능이나 기존에 남아 있는 제품 검증 범위를 확대하지 않는다.

## 조사 결과와 기준 시간

0.2.3 배포의 첫 CI 시작부터 공개 확인까지 약 32분 19초가 들었다. 실패 수정 후 새 실행까지 약 9분, 최종 병렬 후보 준비·검증은 full 기준 약 16분, 게시·공개 확인은 약 6분 30초였다. 병렬 job 시간은 합산하지 않는다.

| 현행 경로 | 확인한 문제 |
| --- | --- |
| `native-manager.yml` | Go `setup-go` 캐시가 이미 있다. 준비와 검증이 다른 runner이며 모든 준비 matrix가 끝나야 검증이 시작된다. |
| `native-tools.yml` | Go 캐시는 있다. 브라우저/full 각각 dispatch하고 후보 안에서 CLI를 다시 만든다. 전체 대상 준비 후 새 runner로 내려받아 검증한다. |
| `build_full_images.py` | 임시 docker-container BuildKit builder를 만들지만 `cache-from/to`를 전달하지 않는다. 역할별 이미지를 같은 job에서 순차 생성한다. |
| 기존 `release.yml`, `scripts/build/*` | Go 및 GHA BuildKit 캐시가 있다. 최근 0.2 게시 경로와 분리되어 해당 최적화가 적용되지 않는다. |
| `publish.yml` | 세 성공 run이 전부 같은 커밋이어야 한다. OCI tar를 다시 내려받아 이미지 10개를 복사한다. |
| `internal/tools/release.go` | CLI·activation·worker가 공통 릴리스 상수를 사용한다. |
| `catalog.go`, `config.go`, 관리 store/install/transaction/full payload | 모든 모듈이 구성의 단일 release와 같아야 한다. 이전 모듈을 섞으면 거부한다. |
| 후보·패키지·이미지·게시 스크립트 | 버전, ZIP 이름, 공개 URL, 이미지 LABEL까지 0.2.3을 직접 포함한다. |

따라서 캐시를 추가하거나 workflow 파일만 합치는 것으로 CLI 변경의 full 재빌드를 없앨 수 없다. 먼저 결과물 식별과 호환성 모델을 분리해야 한다.

## 결과물과 호환 계약

1. 사용자에게 보이는 릴리스 버전은 `loki 0.2.x` 하나로 유지한다. 이 버전은 CLI와 배포 카탈로그의 버전이다.
2. 모듈은 자신의 artifact 버전과 digest를 가진다. 변경 없는 모듈은 이전 버전·URL·digest를 그대로 유지한다. 관리 설정은 카탈로그 버전과 각 설치 모듈의 artifact 식별자를 구분한다.
3. CLI 버전과 별도로 activation, worker 제어 프로토콜, full payload, catalog/state schema의 호환 계약을 명시한다. 버전 문자열 같음 대신 명시적 지원 계약과 의존성 제약으로 구성 가능 여부를 판정한다.
4. 릴리스 구성 lock에는 전체 대상별 CLI, 모듈, OCI 이미지, 의존 모듈 digest, 계약 식별자, 생산 커밋, 빌드 입력 fingerprint, 원래 네이티브 검증 근거를 기록한다. 새 릴리스 커밋과 재사용 결과물 생산 커밋은 각각 기록한다.
5. 재사용 근거는 만료 가능한 Actions artifact가 아니라 공개 릴리스의 불변 파일과 digest 기반 OCI manifest다. Actions artifact는 실행 중 job 전달에만 사용한다.
6. 검증된 기존 모듈 ZIP 내부 manifest를 수정하거나 새 버전이라고 표시하지 않는다. 새 카탈로그가 이전 모듈을 명시적으로 참조한다. 동일 이름 파일을 새 릴리스에 복제하는 작업도 기본 경로에서 제거한다.
7. schema 변경 시 기존 0.2 설치의 버전·선택·ownership 데이터를 새 모델로 읽고 변환하는 절차를 작성한다. 업그레이드 도중 실패하면 이전 데이터와 실행 파일로 복구한다. 실행 중 full 배포는 자동으로 변환하거나 재시작하지 않는다.
8. 기존 릴리스에는 새 fingerprint/계약 근거가 없으므로 개편 첫 실행은 전체 빌드로 새로운 기준 lock을 만든다. 그 이후 선택적 재사용을 적용한다.

## 변경 판정

`plan` job이 마지막 성공한 공개 릴리스 lock을 읽고 현재 소스의 fingerprint와 비교한다. 최근 push의 diff만으로 판정하지 않는다. rename/delete, 여러 커밋, 이전 실패 배포 이후 누적 변경까지 포함한다.

fingerprint는 구성요소마다 별도 계산한다. 파일 경로·내용·실행 비트, 실제 빌드 recipe와 인수, dependency closure, Go/toolchain 버전, 대상 OS/arch/mode, upstream 입력의 버전·digest·notice, base/frontend/BuildKit digest, 계약을 포함한다. 전역 HEAD SHA와 사용자 릴리스 버전은 모든 구성요소를 일괄 무효화하는 key로 사용하지 않는다. 구성요소 자체의 artifact 버전과 실제 바이너리 stamp는 해당 fingerprint에 포함한다.

Go closure는 고정된 빌드 옵션과 대상별 `go list -deps -json`으로 구한다. embed 파일·비 Go 파일·build tags·CGO 설정도 포함한다. 네이티브 runner가 해당 대상의 closure를 다시 확인하며, plan의 대상별 closure와 불일치하면 중단하고 판정 로직을 수정한다. package 경로만으로 공유 코드 영향을 추측하지 않는다.

| 변경 | 빌드와 검증 범위 |
| --- | --- |
| 문서만 변경 | 문서/메타데이터 검사. 새 제품 바이너리 빌드 없음. 버전 변경 없는 일반 CI는 게시 없음. |
| 릴리스 번호·노트 | 새 CLI stamp와 카탈로그/설치기 생성. 모듈·이미지는 재사용. |
| CLI 명령·help·upgrade | CLI 6개 대상. 공유 import 변경이면 실제 영향을 받는 worker/module까지 전파. |
| 설치기만 변경 | 웹 설치기 재생성·필요한 네이티브 설치 확인. bundled install.sh/install.ps1은 해당 OS CLI ZIP 입력이다. 새 릴리스 번호가 필요하면 CLI stamp만 갱신한다. |
| 독립 browser wrapper/engine 입력 | 영향받은 browser 대상. full 이미지에 포함되는 입력이면 Linux browser 이미지·패키지까지 전파. |
| workspace/github/secrets/sharing/coordination 모듈 | 해당 모듈, 그 변경을 컴파일한 worker 및 의존 이미지/구성 검증. 디렉터리 이름만 보고 한 모듈에 한정하지 않는다. |
| execution/Git payload | 영향받은 실행 바이너리·패키지·workload/git-workload 이미지. |
| runtime-core/activation 계약 | 공유 계약을 쓰는 구성요소 및 전체 composition 검증. |
| pinned base/trust 입력 | 이를 사용하는 이미지 전체. browser native closure면 browser 대상만 추가. |
| Go 의존성·toolchain·공통 빌드 recipe | 실제 closure에 영향받는 대상. 판정할 수 없는 변경은 전체 빌드. |
| 테스트/검증 스크립트만 변경 | 해당 검증 재실행. 제품 바이트 불변이면 기존 바이트 재사용. |

빌드 fingerprint와 검증 fingerprint를 분리한다. 검증 스크립트 변경은 과거 통과 근거를 무효화하지만 제품 전체 재빌드 조건으로 삼지 않는다. 모르는 파일, 불완전 closure, 잘못된 lock은 보수적으로 빌드를 늘린다. 재사용 파일이 삭제됐거나 digest가 다르면 재사용을 중단한다. 빌드 계획을 다시 구성할 수 없으면 게시를 중단한다.

## 한 워크플로의 job 구성

```text
plan
  ├── manager-build [필요한 6개 대상]
  ├── browser-build [필요한 5개 대상]
  └── full-build [필요한 Linux arch 2개; payload는 한 번, role은 arch별 2개 병렬]
              ↓
       assemble [새 ZIP + 원래 immutable URL/증거]
              ↓
       source-checks / manager-checks / browser-checks / full-checks
              ↓
       gate [선택된 모든 성공 및 exact lock에 묶인 네이티브 증거]
              ↓ (publish=true)
       publish [Releaseway] → pages → verify-public
              ↓
       summary [실패·취소 때도 실행]
```

- `workflow_dispatch`는 version, `publish`(기본 false), `force_rebuild`(기본 false)를 받는다. maintainer 명령은 선택적 expected_sha로 로컬 검증 커밋도 묶는다. 직접 UI 실행에서는 이를 비워 둘 수 있다. `plan`은 expected_sha가 있다면 checkout SHA와 일치해야 진행하고 모든 job이 그 SHA를 사용한다.
- 같은 파일로 PR/push의 판정·소스 검사를 지원한다. 네이티브 release 후보 생성은 dispatch에서 수행하고 PR/push가 게시 권한을 얻지 않게 한다.
- 기존 조사 전용 워크플로는 목적을 유지한다. 현재 배포를 수행하는 native manager/tools, reviewed-inputs, publish, bootstrap의 중복 dispatch 경로는 통합 후 제거한다. 기존 release.yml의 0.1 배포 graph는 현재 0.2 구조로 교체한다.
- `manager-build`, `browser-build`에서 대상별 준비를 수행한다. 준비 시 source closure와 digest를 검사하고 제품 실행 검사는 조립 후 수행한다. 공개용 artifact는 필요한 ZIP과 receipt만 올린다. 전체 대상 준비를 기다리는 barrier와 후보에 CLI를 중복 컴파일하는 경로를 제거한다.
- 최종 제품 검증은 조립 이후 대상별 `*-checks`에서 수행한다. 이전 전체 후보 디렉터리 대신 그 대상에 필요한 ZIP과 receipt만 내려받는다. full OCI tar를 runner 간 반복 전송하지 않는다.
- full payload는 모듈/arch별 한 번 생성하고 이를 의존 image role들이 참조한다. 이미지 역할은 기존 `ROLE_MODULES`와 실제 programs/recipe를 기준으로 dependency graph를 만든다. 같은 worker나 payload를 role마다 다시 컴파일하지 않는다.
- full 이미지 role/arch를 병렬 실행하되 초기 최대 동시 실행 4개로 registry/cache 경합을 제한한다. full-build는 arch별 job 2개와 각 job의 role thread 2개로 최대 4개를 실행한다. manager/browser는 각 현재 지원 대상 수를 상한으로 둔다. 측정 후 조정한다.
- 입력 receipt 생성·검증을 job 내부로 이동한다. 필요한 대상의 입력만 다운로드한다. `reuse`는 실제 재사용 byte 검사와 계약 확인을 생략하지 않는다.
- 빈 matrix는 job-level `if`로 건너뛴다. 조립/gate는 `!cancelled()`와 명시적인 각 needs 결과 판정으로 실행 여부를 결정한다. plan이 선택한 job은 모두 성공해야 하고, 선택하지 않은 job만 skipped를 허용한다. skipped를 성공으로 취급하거나 `always()`만으로 게시하지 않는다.
- 공개 릴리스 실행은 concurrency group 하나와 `cancel-in-progress: false`로 직렬화한다. 게시 직전 main HEAD와 계획 SHA를 재확인한다. 일반 검사는 별도 ref 기준으로 최신 실행을 유지한다.
- checkout/action/toolchain pin을 유지한다. 빌드 job은 contents read, 이미지 후보 전송 job만 packages write, 최종 게시 job만 contents write, Pages job만 pages/id-token write를 갖는다.

## 캐시 설계

| 대상 | 저장소·key·무효화 | 처리 |
| --- | --- | --- |
| Go module download | Actions cache, OS·arch·Go 버전·go.sum | setup-go는 toolchain만 설치한다. go env로 GOCACHE/GOMODCACHE를 읽고 Actions cache가 job·OS·arch·Go·go.sum prefix와 source hash suffix를 사용한다. |
| Go compile/test | Actions cache, OS·arch·Go·build tags·CGO·go.sum·구성요소 source hash | dependency/toolchain prefix로 이전 build cache를 복원하고 Go 자체 content 검증으로 변경 package만 다시 컴파일한다. 최종 ZIP은 이 캐시에 두지 않는다. |
| Node/Playwright/Chrome/DevTools 및 native closure 입력 | Actions cache, 대상·입력 archive digest·receipt schema | 잠긴 원본 다운로드만 캐시한다. Ubuntu Git/OpenSSH·trust·browser library closure는 관리자 측의 인증된 APT 해석으로 버전·URL·byte 길이·SHA-256·InRelease 근거를 recipe에 먼저 고정했다. native job은 index를 다시 해석하지 않고 그 exact bytes만 취득한다. 매 복원에서 길이·digest·notice를 확인한다. 큰 입력은 별도 key로 분리한다. |
| OCI layers | GHCR registry BuildKit cache, image role·arch·cache schema별 ref | 큰 full 이미지 레이어는 registry cache를 사용한다. `cache-from/to type=registry`, 초기 `mode=max`; main dispatch의 신뢰된 빌드만 cache를 저장한다. role/arch가 서로 덮어쓰지 않게 분리한다. |
| 결과물 재사용 | 공개 릴리스 lock 및 ZIP/OCI digest | cache와 독립적. 캐시가 전부 삭제돼도 검증된 공개 바이트를 그대로 사용할 수 있다. |

Go 캐시 key에 릴리스 버전이나 전체 HEAD를 고정 prefix로 넣지 않는다. Actions cache의 immutable key 때문에 변경 소스별 suffix를 사용해 저장하고 좁은 toolchain/dependency prefix로 복원한다. matrix/job의 동일 key 저장 경쟁은 하나의 지정 producer로 제한한다. 서로 다른 OS/arch의 캐시는 공유하지 않는다.

이미지 생성은 pinned docker-container BuildKit과 Dockerfile frontend를 계속 사용한다. `build_full_images.py`가 허용된 cache 옵션만 받아 전달하도록 변경하고 registry 인증은 임시 DOCKER_CONFIG로 주입·정리한다. cache ref는 제품 이미지 digest와 별도 namespace로 둔다. build network 제한은 유지한다.

Dockerfile은 base → trust/native library closure → 실제 payload → 최종 설정/LABEL 순서로 구성한다. 안정적인 큰 레이어 앞에 release LABEL이나 매번 바뀌는 receipt를 놓지 않는다. tar/ZIP은 파일 순서·시간·권한을 정규화한다. 실행할 때마다 바뀌는 절대 경로·생성 시각을 payload에 넣지 않는다.

cache 읽기/쓰기 시간 제한을 명시하고 실패는 경고 후 정상 cold build로 이어진다. 인증·digest 오류와 캐시 exporter 오류를 구별한다. exporter에 의한 늦은 종료가 0.2.3의 빌드 시간을 다시 늘리지 않도록 초기 import/export budget을 각각 60초로 두고 final 검증에서 조정한다. mode=max의 용량과 전송 비용이 이득보다 크면 role별 mode=min으로 줄인다.

GHA BuildKit backend는 기존 경로에 이미 있지만 신규 full 경로에서는 registry backend를 사용한다. GHA quota에 대형 OCI 레이어와 Go/browser 캐시가 경쟁하지 않도록 한다. 용량, 마지막 사용 시각, 전송 시간, cache hit/miss를 summary로 남긴다. 캐시 정리는 전용 cache namespace에만 적용하며 공개 digest가 참조하는 제품 결과물은 건드리지 않는다.

## OCI 전송과 게시

1. 변경된 이미지 job은 exact OCI digest와 receipt를 만든다. 전용 후보 tag로 GHCR에 전송하고 registry의 raw manifest digest를 다시 확인한다. 이 단계는 릴리스 공개가 아니라 빌드 중간 결과 저장이다.
2. native full acceptance는 후보 image를 digest로 받아 검사한다. publisher에는 OCI tar 대신 digest·receipt·검증 결과만 전달한다.
3. 변경 없는 이미지에는 전송·새 이미지 태그 생성을 수행하지 않는다. 새 카탈로그가 기존 immutable digest를 참조한다.
4. 새 이미지도 acceptance에서 쓴 동일 digest를 카탈로그에 기록한다. Publisher가 다시 build하거나 OCI export를 변환하지 않는다.
5. 공개 lock은 신생 결과물의 현재 실행 job 근거와 재사용 결과물의 원래 생산/검증 근거를 함께 기록한다. 전 구성요소가 같은 head SHA라는 기존 검증을 이 근거 검사로 교체한다.
6. 모든 필요 검증이 성공해야 draft release에 파일을 올리고 stable/latest로 전환한다. installer/Pages는 release 자산에서 생성한 동일 바이트를 배포한다.
7. 게시 재시도는 tag·asset·digest가 계획과 같으면 이어서 수행한다. 다르면 중단한다. immutable 공개 릴리스를 덮어쓰지 않는다. Pages나 공개 확인 실패는 이미 게시된 상태를 summary에 분명히 표시한다.
8. 버전별 후보 tag와 cache는 정리 대상으로 구분한다. 제품 lock이 참조한 manifest/blob은 유지한다. 태그 삭제에 따른 registry GC의 영향을 확인하기 전에는 후보 manifest를 자동 삭제하지 않는다.

## Releaseway 적용과 외부 도구 버전

2026-10-04에 조직의 모든 공개 저장소와 최신 stable release의 README/action/workflow 계약을 확인했다. 실행용 Action과 수용성 검증용 fixture를 구분했다.

| 도구 | 최신 stable / commit | 현재 Loki 적용 |
| --- | --- | --- |
| `releaseway/actions` | v0.3.0 / `31c98fec4bbf03f4e179c2a9b3031a50d771a1d3` | GitHub Release lifecycle, draft 재개, 원격 tag/commit 확인, exact asset set/SHA-256/immutability 검증, 재실행 확인, publication/notes 결과 기록에 사용한다. 업로드는 최대 4개를 동시에 진행하고 upload·verify-assets·publish·verify-publication 시간을 기록한다. |
| `releaseway/homebrew-actions` | v0.2.0 / `4bbfa6aafc92a6cb8e823a51d54a6573237e0a76` | 현재 Loki Formula/tap 배포 설정이 없으므로 현행 ZIP/웹 설치기 배포에는 필요하지 않다. Homebrew 채널을 추가하는 작업에서는 native Formula 검사와 tap 게시를 이 도구가 소유하도록 한다. |
| `releaseway/npm-actions` | v0.4.0 / `269fbdeac83fed2df2880915726ed91edbe1bbdd` | 현재 browser package는 private이며 실행용 npm launcher가 없다. 내부 upstream npm 입력 취득은 npm 게시 기능과 다르다. npm 배포 채널을 추가할 때 native launcher와 Trusted Publishing을 이 도구가 소유하도록 한다. |

`release-fixture`, `homebrew-tap-fixture`, `npm-actions-fixture`는 Releaseway 자체 수용성 검증 저장소다. `homebrew-tap-starter`는 신규 tap 초기 템플릿이며 현행 Loki의 배포 runtime dependency가 아니다.

Releaseway가 버전 선택·tag 생성·제품 빌드·OCI 게시·Pages를 수행한다고 가정하지 않는다. Loki는 그 책임을 유지하고 `publish` job의 GitHub Release 조작만 action에 맡긴다. 원격 tag는 `publish-tag.sh`로 먼저 생성/확인하고 accepted commit을 정확히 전달한다. action에 필요한 권한은 contents write이며 npm/tap 자격증명을 GitHub Release job에 추가하지 않는다.

릴리스 노트는 현재 조립한 `notes: file`을 전달하고 `notes-existing: verify`로 기존 draft/published 본문과 동일성을 확인한다. 자동 commit 노트 생성은 기능 설명·설치 안내를 의미 기반으로 생성해 주는 기능이 아니므로 현재 제품 노트를 유지한다. action의 state/release-url/notes-report를 summary와 진단 artifact로 보존한다. 중복 create/upload/edit와 공개 asset 교체 로직은 추가하지 않는다.

외부 Action·Go·Node·BuildKit·frontend·native base·browser engine/Chrome·도구 입력은 구현 시 공식 upstream의 최신 안정 버전을 확인해 정확한 version/commit/digest로 고정한다. 이전 근거 파일은 보존하고 새 입력 receipt를 만들어 영향받은 closure를 다시 검증한다. 실행 도중 upstream 최신 버전을 자동으로 골라 받아 동일 계획의 결과물이 달라지게 하지 않는다. 최신 버전 여부와 Action 계약 검사는 final 검증에서 수행한다.

Releaseway는 단일 `release.yml`의 `publish` job에서 사용하며 이전 6개 dispatch 워크플로는 제거했다. 기존 성공 릴리스 evidence는 바꾸지 않으며 이 소스 변경을 실제 공개 검증 통과로 기록하지 않는다.

실패한 job만 재실행하면 이전 attempt의 성공 증거를 유지한다. 증거는 같은 run ID·source SHA·release·검증 대상·조립 전 lock SHA-256에 묶이며 미래 attempt나 다른 cohort는 거부한다. 이미 공개된 버전의 exact source로 전체 재실행하면 공개 asset digest 목록으로 원래 바이트를 복원해 Releaseway verify와 Pages를 이어간다. 같은 버전에 source가 바뀌면 publish=true를 차단하고 다음 patch를 요구한다. publish=false의 같은 버전 테스트는 별도 validation-only 구성이다.

최신 pin 확인(2026-10-04): Go 1.27.1, Node 26.10.0, Chrome 154.0.8037.92, Playwright MCP 0.0.83, Chrome DevTools MCP 1.10.1, gh 2.102.0, devtools 0.23.2, BuildKit 0.33.1, buildx 0.37.2. full Git/OpenSSH는 Ubuntu 24.04의 최신 인증된 배포 패키지와 ABI closure를 고정한다. Actions는 최신 stable commit SHA로 고정한다.

## 구현 순서

실행 검증은 아래 최종 검증 단계에 모은다. 각 구현 단계에서는 해당 검증 코드·fixture·명령·실패 조건까지 준비하고, 구현 전달과 검증 통과를 구분한다.

1. **릴리스 입력 정리:** 현재 version literal/receipt/contract와 실제 source closure의 소유권을 조사표로 고정한다. 버전·노트·recipe는 구조화된 한 입력에서 읽도록 하고 hardcoded 0.2.3을 제거한다.
2. **계약과 state 분리:** CLI release, catalog release, module artifact version, protocol/schema 계약을 분리한다. catalog/install/update/store/full activation과 worker의 검증을 새 모델에 맞춘다. upgrade 변환·ownership·복구 fixture를 작성한다.
3. **구성 lock과 선택 계획:** fingerprint, dependency propagation, build/test/reuse 판정과 동적 matrix 생성 스크립트를 구현한다. 입력별 판정 이유를 출력하고 baseline이 없는 첫 배포는 전체로 확정한다.
4. **중복 작업과 캐시:** manager 한 번 빌드, payload 한 번 생산, image 역할별 병렬화, 입력/Go/registry caches, deterministic packing과 byte 전송 경로를 구현한다.
5. **단일 job graph:** release.yml에 모든 job을 옮긴다. 조건부 matrix와 gate를 구현하고 별도 dispatch/run ID 연결을 제거한다. 기존 0.2 실행형 워크플로와 낡은 0.1 graph를 정리한다.
6. **증거 조립과 게시 복구:** mixed producer commits를 허용하는 immutable provenance 검사, 카탈로그 생성, 게시 재시도, Pages/public checks, summary·cache/critical-path 계측을 구현한다.
7. **문서와 검증 준비:** maintainer 명령, 수동 force rebuild, 재시도 방법, lock/계약 schema, cache 삭제 후 동작, 실제 예상 job 표를 문서화한다. devtools 구현/최종 검증 태스크를 이 순서로 연결한다.
8. **최종 검증:** 아래 검증을 실행하고 누락·실패를 고친다. 성공 근거를 고정한 뒤 배포 가능한 상태로 완료한다. 이 계획 작성 자체는 배포하지 않는다.

## 최종 검증과 완료 기준

### 판정과 의존성

- CLI/help만 수정, installer만 수정, browser 입력만 변경, Git/execution payload 수정, 공통 worker 변경, go.sum/toolchain/base 변경, 문서/테스트만 변경 fixture를 실행한다. 생성된 build/test/reuse 대상이 위 표와 실제 dependency closure에 일치해야 한다.
- 삭제·rename·embed·build tags·CGO·여러 커밋·이전 실패 이후 변경을 포함한다. 알 수 없는 입력은 필요한 전체 범위를 선택해야 한다.
- release 번호만 바꾼 계획에서 browser/full artifact·이미지 digest가 모두 유지되어야 한다. baseline 없는 첫 실행은 전체를 선택해야 한다.
- 필수 job 실패/취소, 빈 matrix, 허용된 skipped, 누락 receipt를 검사한다. 필요한 job 하나라도 실패하면 게시 job이 실행되지 않아야 한다.

### 계약·state·재사용

- 기존 0.2.3 CLI-only와 선택 도구 설치를 새 모델로 업그레이드한다. 선택·설정·ownership·실행 중 배포를 보존하고 중단/재시작/롤백을 확인한다.
- 새 CLI + 이전 browser/full, 새 모듈 + 이전 의존 모듈, 잘못된 protocol/schema 조합을 시험한다. 지원 계약은 성공하고 불일치는 설치 전 차단되어야 한다.
- 공개 ZIP/digest/notice/target/producer evidence가 변조되거나 사라진 경우 재사용·게시가 거부되어야 한다. 캐시 적중을 검증 통과로 기록하면 안 된다.
- 기존 모듈 바이트를 새 릴리스 카탈로그에 넣어 실제 install/update/serve/full lifecycle를 검사한다. 새 버전으로 ZIP을 다시 싸지 않아도 동작해야 한다.

### 캐시와 실제 네이티브 제품

- Go/Python/recipe 단위검사와 YAML/actionlint를 실행한다. workflow job 그래프와 pinned action 입력을 확인한다.
- `publish=false`로 실제 6개 CLI, 5개 browser, 2개 Linux full 기준 실행을 진행한다. 실제 Windows 교체/복구, PowerShell 5.1 IEX, macOS 경로 alias, Linux sandbox, 브라우저 양 engine, full composition 검증을 유지한다.
- 같은 입력으로 두 번째 실행, CLI만 변경한 실행, browser만 변경한 실행을 수행한다. cache 복원 로그뿐 아니라 실제 컴파일/레이어 hit와 skipped job을 확인한다.
- cache 없는 실행과 cache 있는 실행을 비교한다. checksum·notice·계약·프로토콜 검증 결과가 일치해야 한다. cache backend 지연/실패가 cold build로 복구되어야 한다.
- 판정된 CLI-only 릴리스는 full image build 0개, browser rebuild 0개, OCI tar 전송 0개, 변경 없는 이미지 재게시 0개가 필수 완료 조건이다. 공유 코드 변화로 실제 worker가 달라진 경우에는 그 사유가 plan에 표시되어야 한다.
- 각 job의 queue/setup/input download/build/test/cache import/export/artifact transfer/publish 시간을 기록한다. warm full 실행은 cold full보다 빨라야 하며 cache 비용이 이득을 넘으면 scope/mode를 조정한다.
- 성능 목표는 CLI-only warm dispatch 시작부터 게시 준비까지 8분 이내, 게시·공개 확인 2분 이내다. runner queue는 별도 보고하고 최소 두 번 측정해 비교한다. 공개 baseline 반복 측정에서 초기 대기 제외 준비 시간은 두 번 모두 8분 이내였다. CLI-only 게시 시간은 미측정이며 첫 전체 배포의 공개 전환 시간과 병목은 별도 증거에 기록했다.

### 게시와 전환

- draft 단계 실패와 release 공개 후 Pages 실패를 각각 재현한다. 성공한 job/불변 byte를 유지한 재시도가 중복 업로드·재빌드·잘못된 latest 전환을 만들지 않아야 한다.
- 게시 직전 main 변경, 동시 release 요청, 같은 tag 다른 byte를 차단한다. publish=false에는 제품 release/Pages 전환이 발생하지 않아야 한다. 후보 registry/cache 저장은 summary에 명시한다.
- 최종 승인된 공개 실행 후 installer byte 일치, CLI 실제 설치/upgrade, 재사용 module 실제 설치, OCI raw digest를 익명으로 확인한다.
- 활성 배포 진입점이 release.yml 하나인지 확인한다. 별도 run ID 입력 없이 계획부터 공개 확인까지 한 Actions 실행에서 추적 가능해야 한다.

## 참고

- [Docker registry cache](https://docs.docker.com/build/cache/backends/registry/): 독립 cache ref와 mode=min/max.
- [Docker GHA cache](https://docs.docker.com/build/cache/backends/gha/): image별 scope, 수동 buildx의 인증 환경, timeout과 API 제한. 기존 경로 조사 근거.
- [actions/setup-go](https://github.com/actions/setup-go): Go module/build cache 지원.
- [GitHub job dependencies](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-jobs): needs의 skipped/failed 전파와 조건부 job 처리.
- [Releaseway GitHub Release v0.3.0](https://github.com/releaseway/actions/tree/31c98fec4bbf03f4e179c2a9b3031a50d771a1d3): exact assets, immutable lifecycle, notes, 재개 및 제한된 병렬 업로드 계약.
- [Releaseway Homebrew v0.2.0](https://github.com/releaseway/homebrew-actions/tree/4bbfa6aafc92a6cb8e823a51d54a6573237e0a76): Formula/native 검사와 tap 게시 계약.
- [Releaseway npm v0.4.0](https://github.com/releaseway/npm-actions/tree/269fbdeac83fed2df2880915726ed91edbe1bbdd): npm Trusted Publishing과 native launcher 계약.
- [0.2.3 게시 실행](https://github.com/jinyongp/loki/actions/runs/37201234614), [현재 릴리스 근거](evidence/release-publication.json).
