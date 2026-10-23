# 릴리스 검증 최적화 조사 결과

기준: `4f1d66fe202e125d2354250d10a655462bd3d66c`, devtools workstream `9c553662-2eec-4b99-93c0-0c00b4ee5300`, 계획 definition revision 1034. 조사일 2026-10-24 KST. 제품 코드·workflow·배포 상태는 변경하지 않았다.

## R01: 비용과 경로

워크플로 7개를 확인했다. `release.yml`만 main push/PR 검사와 dispatch 릴리스를 담당한다. `secret-integration.yml`은 별도 branch/dispatch 통합 검증이다. browser-final-inspection, full-retained-acceptance, macos-input-inspection, public-install-smoke는 수동 진단이며 dependency-watch는 주간 제안 작업이다. 이들을 삭제해도 일반 릴리스의 시간이 줄지 않는다.

로컬 진입점은 release.sh → verify-preflight.sh → verify-source.sh 및 verify-race.sh다. 기본 source는 전체 테스트·vet·cmd build·architecture·tidy·diff, race는 전체 Go 패키지를 검사한다. 선택적인 `--oci`는 실제 OCI 검증을 추가한다. CI는 plan → 선택적 manager/browser/full build → assemble → source/manager/browser/full checks → gate → 명시적 publish → Pages → public verify다. source-checks는 산출물을 소비하지 않지만 현재 assemble을 기다린다. 각 native check도 모든 build matrix를 기다린 뒤 시작한다. 이 전체 barrier를 없애는 설계는 이번 최소 변경안에 포함하지 않는다.

실행 6건, 그 안의 모든 보존된 attempt와 job/step 메타데이터를 수집했다. 성공 3건(38996724241, 38851251342, 38777137633), 실패 1건, 취소 1건, 조사 시 진행 중인 실행 1건이다. 원자료는 `run-*.json`, 요약은 `history-summary.json`이다. 같은 이름·시작·종료 시각의 보존된 성공 job을 attempt마다 중복 집계하지 않았다. skipped의 합성 시각과 역전된 inherited created/start 시각은 실행비용/queue로 해석하지 않았다. reducer의 정상/역전/누락/skip 예제 2개가 통과했다.

| 실행 | source-checks job | gate job | gate 전체 파일 다운로드 | publish job |
| --- | ---: | ---: | ---: | ---: |
| 38996724241 성공 | 75초 | 64초 | 49초 | 157초 |
| 38851251342 성공 | 53초 | 47초 | 37초 | 212초 |
| 38777137633 성공 | 53초 | 44초 | 33초 | 275초 |
| 39984988033 진행 중 snapshot | 62초 | 1,171초 | 1,154초 | 미완료 |

최근 실행의 assemble은 329초 중 281초가 다운로드였다. Linux amd64 browser/full check 다운로드는 509/523초였고 실제 제품 검사 묶음은 각각 148/135초였다. gate 첫 attempt는 다운로드 중 취소되었고, 후속 attempt에서 다시 전체를 받았다. snapshot 시 latest attempt는 3이며 공개 완료로 기록하지 않았다. 진행 중 단계의 최종 시간은 알 수 없다.

세 성공 실행도 소스·선택된 구성·cache가 동일하지 않다. 따라서 위 숫자는 분포 관찰이며 최적화의 인과적 성능 비교가 아니다. 외부 저장소 전송의 정확한 저속 원인은 현재 job/step 메타데이터만으로 구분할 수 없다. cache hit/miss와 일부 내부 대기의 세부 시간도 미상이다. 대형 파일을 다시 내려받아 이 현상을 재현하지 않았다.

`decision-rules.json`을 candidate 실험 전에 고정했다. 실제 구현 후 조건을 맞춘 A B B A 순서의 최소 2회씩 비교, elapsed median 10% 감소 목표, runner 시간 5% 초과 회귀 금지다. 표본 범위가 겹치거나 workload/cache가 다르면 시간 효과는 판정 불가다. 이번 조사에서 그 목표 달성을 주장하지 않는다.

13개 native check가 받는 artifact family를 actual compressed byte 크기로 계산했다. 현재 총량 10,400,043,972 bytes, 소비하는 family만 받는 투영은 3,782,037,744 bytes다. 불필요한 6,618,006,228 bytes(약 63.6%)를 제거할 수 있다. 실제 네트워크 측정이 아니며 전체 신규 빌드 snapshot에 대한 전송 구조 계산이다. CLI-only/재사용 실행의 절감량으로 일반화하지 않는다. 상세 행은 `transfer-projection.json`에 있다.

## R02: 보호 실패와 중복

검토 범위는 배포 진입점의 검증 profile, release.yml의 모든 검사 영역, 직접 호출하는 native acceptance 진입점과 브라우저 callback이다. 개별 전체 Go 테스트를 삭제 후보로 전수 판정한 것은 아니다.

| 영역 | 보호 실패 | 판정 |
| --- | --- | --- |
| 로컬 source / CI source | 테스트·소스·핀·선택 계획 계약 | 유지. 로컬 vet/architecture/tidy와 CI 명령 범위가 다름 |
| 전체 로컬 race / CI 부분 race | 메모리 경쟁, 대상 패키지 차이 | 유지. 통과 근거 동등하지 않음 |
| manager native / upgrade / installer / system-host | 실제 네이티브 실행·기존 상태·설치·권한 경계 | 유지 |
| browser 기본 / capabilities 내부 기본 | 같은 accept 함수와 기본 protocol callback | 독립 기본 호출 통합 후보 채택 |
| browser installer connection | 실제 저장된 연결 인자·호스트 고정·사용자 설정 보존 | 유지. 직접 serve와 다른 경계 |
| browser 최소 배포판 / macOS·Windows | 시스템 의존성·복구·플랫폼 동작 | 유지 |
| full workspace / full composition | 격리된 workspace 권한·작업·연결·폐기 | 유지. 모듈/권한이 다름 |
| gate / publisher / public verify | 증거·실제 byte·게시·공개 설치기 경계 | 유지. 전송 구조만 별도 개선 |

`contract_experiments.py`는 실제 기존 protocol과 optional_protocol을 호출하되 process/network 경계는 가짜 Client로 주입했다. 정상 대조 후 중복 도구명, unsafe 노출, 파일 반환 바이트, 비활성화 후 호출 허용, Playwright snapshot, DevTools snapshot, 선택 page, PNG 누락, 잘못된 PNG의 9가지 대표 결함에서 독립 기본 호출과 retained callback이 같은 오류를 감지했다. optional 전용 분기는 시작 직전 sentinel로 멈추므로 이 실험은 optional 전체나 실제 패키지 수용을 검증하지 않는다. 코드상 capabilities는 동일 accept()로 설치·상태·doctor·정리도 재수행한다. 실제 5개 browser target의 확인은 구현 후 필요하다.

full acceptance는 고정 호스트 Docker socket, Windows installer는 사용자 PATH를 사용하는 경로가 있어 연구 중 원본 제품/설치기를 실행하지 않았다. native acceptance를 제거하지 않고 해당 미실증을 후속 구현의 필수 검사로 남겼다.

## R03: 전체 재빌드 판정

공개 v0.2.20의 lock byte 길이·GitHub asset SHA-256을 확인하고 계획 artifact ZIP digest를 확인한 뒤 source를 비교했다. baseline은 be8cd5489f6dc18e2efa394ad8a56f0ee1264ccc다. 계획의 39개 unit은 모두 forced이며 changed-unmapped는 15개다. 전체 unknown 200개 목록과 matrix는 `selection-observation.json`에 보존했다. 200개를 전부 무해하다고 판정하지 않았다. 실제 변경된 15개는 아래 경계로 추적했다.

| 경로 | 소비 경계 | 이번 결론 |
| --- | --- | --- |
| .dockerignore, compose.yaml | 기존 image/Compose·별도 제품 검증 입력 | blanket 제외 금지. 별도 closure 정의 전 유지 |
| native-migration/main.go, native-reboot/main.go | accept-secret-compose/reboot의 실제 VM fixture | 별도 통합 검증 입력. 기본 source 검증만으로 대체 금지 |
| private-value-probe/main.go | accept-secret-compose | 별도 통합 검증 입력 |
| oci-fixture-server/main.go, oci-image/Dockerfile, egress-policy.json | 실제 OCI/secret job fixture | 별도 통합 검증 입력 |
| provider-expired/main.go | secret-integration의 package fixture | 별도 통합 검증 입력 |
| internal/secretoutput/LICENSE, provenance.json | 외부 코드 출처/라이선스 및 provenance 테스트 | 제품 배포 notice 소유권 확인 전 유지 |
| packaging/images/Dockerfile | build-oci와 native secret 이미지 | 별도 이미지 입력. 무시 금지 |
| candidate-linux-amd64-full.json, candidate-linux-arm64-full.json | 현행 release_pipeline.full → prepare_candidate | 실제 현행 recipe 입력. 정확한 target/role 매핑 전 유지 |
| scripts/maintainer/lifecycle.sh | 기존 build-candidate 설치 산출물 | 기존 제품 경로. 무시 금지 |

실제 Go closure를 사용하는 기존 선택 회귀 9개가 통과했다. 별도 5개 실험에서 Python test-only는 build 0/0/0, 현재 unknown VM fixture와 candidate recipe는 6/5/2 전체 forced, 삭제된 baseline unknown도 전체 forced임을 확인했다. manager bridge만 변하면 build 0개인데 browser/full 검사만 선택되는 경우도 재현했다. 따라서 새 artifact가 없는 합법적 검사 재실행을 반드시 지원해야 한다.

`release_pipeline.py`는 모든 unit의 공통 fingerprint 입력이다. 이 파일의 전송 helper 수정 자체가 전체 빌드를 유발할 수 있으므로 첫 배포 비용과 이후 선택적 실행 비용을 분리해야 한다. 이번 단계에서는 fallback 변경을 보류한다. 제품/별도 통합 검증 입력을 단순 제외해 전체 빌드 횟수를 줄이지 않는다.

## 실행 확인과 한계

- 기존 test_release_pipeline: 19개 통과.
- 기존 test_release_selection: 9개 통과. 첫 실행은 read-only 기본 Go cache 때문에 setup에서 실패했다. GOCACHE를 /tmp의 연구 전용 경로로 바꾼 재실행이 통과했다.
- protocol/materialize/gate/모형 실험: unittest 6개 통과. 내부에 브라우저 결함 9개, gate 상태/증거 9개 등의 하위 대조가 있다.
- 실제 closure/fallback 추가 시나리오: 5개 통과.
- 시간 reducer: 2개 통과.

합계 41개 테스트/시나리오 단위다. 로컬 Go는 1.27.3이며 CI pin 1.27.2와 다르다. 이 검사는 선택·프로토콜·gate 계약 연구이고 native binary의 호환성/성능 통과를 뜻하지 않는다. 제품·운영 설치·Docker·원격 workflow·release/tag/Pages/GHCR 쓰기는 수행하지 않았다.
