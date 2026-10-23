# 릴리스 검증 최적화 전략

작성일: 2026-10-24 KST. 조사 workstream: `9c553662-2eec-4b99-93c0-0c00b4ee5300`. 기준 제품 소스: `4f1d66fe202e125d2354250d10a655462bd3d66c`.

**P01–P04 구현과 단위별 검증·리뷰·커밋, 13개 실제 native CI 및 gate 부분 재실행을 완료했다.** [구현 검증 기록](evidence/release-validation-implementation/P05-review.md)과 [CI attempt2](https://github.com/jinyongp/loki/actions/runs/40003099710/attempts/2)가 최종 근거다. 새 공개 릴리스/Pages 배포는 수행하지 않았고, matched 비교가 없어 배포 시간 단축률은 판정하지 않는다. 이후 사용자 추가 요청으로 Releaseway v0.8.0·standard 노트 전환과 기존 74개 릴리스 제목/본문 개선도 완료했다. v0.1.0 immutable flag 변화는 [별도 기록](evidence/release-validation-implementation/P07-release-metadata-review.md)에 명시했다. devtools의 최초 미착수 문구는 당시 계획 이력이며 현재 결과는 각 task validation과 이 문서를 따른다.

## 확정된 선택

1. native 검사마다 소비하는 artifact family만 받는다. 전체 신규 빌드 snapshot에서 불필요 수신 약 6.62GB를 제거할 수 있다.
2. browser 기본 검사의 독립 호출을 capabilities 안의 동일 기본 callback으로 통합한다. 실제 installer 연결 검사는 유지한다.
3. source-checks를 assemble 대기에서 분리하고, gate의 필수 job 상태를 대형 다운로드 전에 검사한다. 실패 발견 시간과 성공 실행 시간의 효과를 나눠 평가한다.
4. 별도 단계로 조립 inventory와 gate의 증거 판정, publisher의 최종 byte 확인을 연결한다. 읽기 전용 gate와 게시 권한을 분리하고 publish=false의 실제 byte 검증도 유지한다.
5. unknown fallback 축소는 보류한다. 전체 race, OS/아키텍처, installer 연결, 최소 배포판·full 권한 검사를 줄이지 않는다. gate/publisher를 단일 권한 job으로 합치는 안은 기각한다.

첫 단계의 단순한 개선과 봉인 구조 변경은 독립적으로 검증·커밋·되돌릴 수 있게 나눈다. 봉인 변경을 끝내야만 전송 family 축소를 적용할 수 있는 의존성은 만들지 않는다.

## 근거와 범위

[조사 기록](evidence/release-validation-optimization/20261023T174151Z-9c553662/investigation.md)은 workflow 7개, 로컬 source/race, 릴리스 job graph, native acceptance 경계를 다룬다. 실행 6건과 보존된 attempt 전체를 수집했다. 성공 3건에서 gate 다운로드는 33·37·49초였고, 최근 지연 실행에서는 1,154초였다. 서로 다른 workload·source·cache라 최적화의 인과적 시간 비교는 아니다.

[전송 계산](evidence/release-validation-optimization/20261023T174151Z-9c553662/transfer-projection.json)의 13개 native check 수신 합계는 10,400,043,972 bytes다. 필요한 family만 수신하는 투영은 3,782,037,744 bytes로 약 63.6% 감소다. 이 계산은 actual artifact 압축 크기 기반이며 native 검사 전체의 시간이나 CLI-only 실행 절감량을 뜻하지 않는다. gate 경량화는 publish=true에서 별도로 약 3.73GB의 전체 artifact 수신 1회를 없애는 구조다.

실제 기본 callback에 주입한 9가지 결함은 독립 기본 호출과 retained callback에서 같은 오류를 냈다. installer 연결·부분 race·native full 검증은 다른 실패를 보호하므로 유지한다. 기존 회귀 28개, reducer 2개, protocol/materialize/gate 실험 6개, 선택 경계 5개, 봉인 모형 4개가 통과했다. 합계 45개 테스트/시나리오 단위이며 하위 대조는 별도다. 로컬 Go 1.27.3과 CI pin 1.27.2가 다르고, 모형 결과는 실제 native/Actions/Releaseway 실행 수용을 대체하지 않는다.

최근 force_rebuild=false 실행에서 39개 unit이 forced였다. 15개 변경된 unknown에는 실제 recipe·이미지·출처 정보와 별도 native/OCI fixture가 섞여 있다. [선택 조사](evidence/release-validation-optimization/20261023T174151Z-9c553662/R03-selection-review.md)에 따라 blanket 제외를 하지 않는다. unknown 200개를 모두 무해한 입력으로 입증한 것도 아니다.

## 구현 단위와 완료 조건

각 단위는 구현 → 영향받는 검사 → 리뷰루프 → 관련 파일만 커밋 순서로 마쳤다. 구현 workstream은 `fa56f1a5-d777-4bcb-8e43-2bb923be0cc0`이며 P01–P05와 사용자 추가 요청 P06–P07의 근거를 연결했다. 아래 설계는 완료 조건과 향후 회귀 기준으로 유지한다.

### P01: 소비자별 artifact 수신

대상: `.github/workflows/release.yml`, `scripts/maintainer/tools/plan_release.py`의 matrix 출력, 필요한 경우 `release_pipeline.py`의 명시적 획득 메타데이터, `test_release_pipeline.py`, `test_release_selection.py`, `internal/packaging/release_workflow_test.go`.

matrix/check target에 필요한 manager/browser/full artifact와 각 build 여부를 명시한다. manager는 manager만, browser는 manager+project-host browser, full은 manager+full만 받는다. 새 build로 선택한 family는 정확한 artifact 이름으로 다운로드하고 누락 시 실패한다. 이번 run에서 재사용하는 family는 그 다운로드 step을 생략한다. 이후 기존 materialize가 승인된 공개 URL·길이·SHA-256으로 필요한 바이트를 가져오게 한다.

공개 URL fallback은 새 build의 산출물 누락을 숨기는 수단으로 사용하지 않는다. pattern의 빈 매칭을 선택된 build의 성공으로 처리하지 않는다. 서로 다른 family의 units.json·동명 파일이 덮어써지지 않도록 디렉터리 범위를 유지한다. artifact 전체 다운로드와 archive 내부 파일 부분 다운로드를 혼동하지 않는다.

완료 조건: 전체 신규·CLI-only·module-only·검증만 변경·전부 재사용·신규/재사용 혼합·빈 matrix·exact-source recovery에서 필요한 파일만 획득한다. 누락/중복/변조는 거부한다. 13개 native check 경로에서 관계없는 family 수신이 0개이고 필요한 archive의 digest/대상/증거는 보존된다. 초기 수정이 공통 fingerprint를 바꾸면 첫 전체 rebuild 비용을 별도 표시한다.

### P02: 동일 browser 기본 검사 통합

대상: release.yml의 browser-checks 명령, `accept_browser_candidate.py`, `accept_browser_capabilities.py`, 관련 acceptance 테스트와 검증 fingerprint.

독립 `accept_browser_candidate.py` 호출을 제거하되 capabilities가 동일 accept()/기본 protocol을 먼저 실행하는 경로를 유지한다. 기본 discovery·unsafe 기본 거부·두 engine snapshot·파일/resource/PNG 반환·비활성화 후 거부·설치/상태/doctor/정리를 유지한다. public installer가 만든 실제 연결 실행은 그대로 둔다. 미래에 callback 구성이 바뀌면 이 통합의 근거도 재검토한다.

완료 조건: 정상 대조와 대표 결함 9개를 retained 경로가 감지하고, 실제 Linux 2개·Windows amd64·macOS 2개의 browser 수용 검사가 통과한다. 모형만 통과한 상태에서 native 완료를 기록하지 않는다. P01과 독립 구현 가능하지만 공유 YAML 충돌은 같은 작업자가 조정한다.

### P03: 조기 소스·필수 상태 검사

대상: release.yml source-checks/gate, release_gate.py의 현재 required-result 규칙과 테스트.

source-checks의 assemble 의존성을 제거하고 push/PR/dispatch에서 필요한 실행 조건을 유지한다. gate는 plan과 job 결과를 받은 직후 현재와 같은 필수 성공/허용 skipped 규칙으로 판정한다. 필수 실패/취소면 큰 artifact 다운로드를 시작하지 않는다. 증거 검사에 필요한 작은 metadata/check artifact도 큰 파일보다 먼저 처리한다. 필수 결과 판정 규칙을 두 군데에 따로 복제하지 않는다.

완료 조건: 선택된 실패·취소·누락, 선택하지 않은 허용 skipped, 빈 matrix를 기존 gate와 같은 의미로 판정한다. source 실패를 앞서 발견하면서 publish의 gate 성공 요구와 전체 source 범위를 보존한다. 정상 실행의 시간 효과는 실제 critical path에 포함되는 경우만 인정한다.

### P04: inventory 기반 봉인과 게시 전 byte 확인

대상: release_pipeline.py assemble, release_gate.py의 증거/lock 봉인과 checksum 작성 경계, release.yml assemble/gate/publish, Python gate/recovery 테스트, release_workflow_test.go의 현행 전송 토폴로지 assertion.

최종 흐름은 다음과 같다.

```text
assemble: 실제 자산 검증 + 조립 전 lock + per-file inventory
    ↓
native checks: 조립 전 lock digest에 묶인 현재/허용 과거 attempt 증거
    ↓
read-only gate: 필수 결과·증거·inventory 일치 → 최종 lock과 봉인 metadata
    ├─ publish=false: 실제 파일 다운로드 → 동일 verifier → 종료
    └─ publish=true: publisher가 실제 파일 다운로드 → 동일 verifier
                       → main SHA 확인 → tag → Releaseway → Pages → 공개 확인
```

1. assemble에서 모든 실제 public asset의 정확한 이름·byte 길이·SHA-256을 계산한다. inventory에는 schema, source SHA, release, run, 조립 전 lock digest와 unit/asset 연결을 담고 bounded regular JSON으로 취급한다. 새 build 산출물은 현재 plan/producer receipt와, 재사용은 원래 immutable 공개 근거와 일치해야 한다. 현재의 집계형 transfer-inventory는 per-file 증거가 아니므로 그대로 신뢰하지 않는다.
2. inventory는 public assets 바깥의 metadata로 전달한다. Actions artifact ID/digest와 현재 run의 명시적 전달 경로를 유지한다. digest 실패를 경고로 낮추거나 다른 run의 artifact를 임의 탐색하지 않는다.
3. gate는 기존 증거 cohort 판정을 보존하고 조립 전 lock에 acceptance를 추가하여 최종 lock을 만든다. inventory의 lock entry를 최종 lock digest로 갱신한 뒤 SHA256SUMS를 만든다. 이어 SHA256SUMS 자체의 길이/digest도 final inventory에 넣는다. inventory는 자기 자신을 hash하지 않으며 final file set에 포함되지 않는다. accepted metadata에는 최종 lock·SHA256SUMS·final inventory가 포함된다.
4. publisher는 원래 asset payload에 accepted lock·checksum을 적용한 뒤, 정확한 파일 집합과 regular-file/이름·길이·digest를 공통 verifier로 확인한다. 추가·누락·중복·변조 파일은 tag나 Releaseway 호출 전에 거부한다. checksum에 나열된 파일만 검사하고 local glob의 추가 파일을 그대로 게시하지 않는다.
5. publish=false에서는 gate가 전체 파일을 한 번 받아 같은 verifier를 실행한다. 이 모드에서는 gate의 큰 다운로드를 제거하지 않는다. 검증 성공과 게시 효과 0개를 함께 확인한다. publish=true에서만 gate의 추가 전체 수신을 없앤다.
6. 공개 동일 버전 exact-source recovery는 기존 sealed lock/checksum/asset 바이트를 그대로 복원하고 공개 immutable digest로 검증한다. 현재 JSON 직렬화나 새 acceptance를 넣어 원래 공개 바이트를 다시 만들지 않는다. 미래/다른 run·다른 source 증거와 같은 버전 다른 바이트는 거부한다. draft 재개·이미 공개된 상태·Pages만 실패한 상태를 각각 유지한다.

gate는 contents:read, publisher만 contents:write를 유지한다. 같은 job에 합치거나 새 저장 서비스·자체 cache를 추가하지 않는다. 현행 테스트의 "gate가 전체 파일을 받는다"는 토폴로지 assertion은 publish 여부에 따른 byte 검증 보존 assertion으로 바꾸되, 필수 결과·exact cohort·제품 무결성 조건은 완화하지 않는다.

완료 조건: 기존 gate/선택/재사용 회귀와 미래 inventory verifier의 독립 기대값을 만족한다. publish=false와 publish=true 경로 모두 같은 byte verifier를 tag 전에 사용함을 검사한다. 실제 native CI, 실패 job 부분 재실행, 검증 전용 실행을 통과해야 구현 완료다. 실제 공개 게시/복구는 별도 승인된 릴리스의 관측 결과로만 기록한다. 연구 모형 통과나 기존 Releaseway 소스 확인으로 이 단계를 대신하지 않는다.

### P05: 통합 검증과 성능 판정

P01–P04 중 적용한 작업의 결과를 소비한다. 공통 입력·workflow 변경이면 CLI 6개·독립 browser 5개·Linux full 2개의 실제 검증을 실행한다. 임의의 전체 테스트 감소로 성능 목표를 맞추지 않는다. 로컬 full race, 필요한 architecture/tidy/vet, Python 회귀와 workflow parser/actionlint, byte/cohort 변조, 재사용·빈 matrix·recovery·부분 retry를 각 실패 경계에 맞게 실행한다.

최소 한 번의 force-rebuild 검증 전용 실행으로 실제 산출물 전달 전체를 확인한다. CLI-only의 실제 선택/시간 확인은 해당 변경을 포함한 승인된 공개 baseline이 필요한 경우 그 이후로 구분한다. 그 전에 mock baseline으로 얻은 선택 테스트를 실제 CLI-only 배포 성능으로 표시하지 않는다.

성능 규칙은 [사전 기준](evidence/release-validation-optimization/20261023T174151Z-9c553662/decision-rules.json)을 따른다. 가능한 경우 동일 제품 바이트·runner/tool/cache·전송 경계를 맞춘 baseline/candidate를 A B B A 순으로 최소 두 번씩 비교한다. 실행 정책 때문에 실제 end-to-end 비교를 맞출 수 없으면 시간 효과는 판정 불가로 남기고 bytes/횟수의 구조적 개선만 보고한다. 서로 다른 커밋·workload의 역사적 실행을 matched benchmark로 바꾸지 않는다. 중앙값 10% 감소는 목표이며 이미 얻은 성과가 아니다.

## 복구와 적용 판단

각 구현 단위는 리뷰루프와 커밋을 마친 뒤 별도로 되돌릴 수 있어야 한다. P04를 되돌리면 기존 gate 전체 byte 검사와 publisher 재검증 경로로 복구한다. 공개된 immutable 릴리스는 수정하지 않는다. P01·P02·P03은 P04를 되돌려도 독립적인 검증 조건을 충족하면 유지할 수 있다. 성능이 불명확하고 복잡도만 늘어나는 변경은 적용하지 않는다.

누락/변조/잘못된 cohort가 통과하거나 publish=false에서 byte 검증이 사라지거나 권한 범위가 커지면 성능과 관계없이 기각한다. 필요한 검증은 유지한다. 별도 secret/native 통합 워크플로와 기존 미지원 제품 검증 범위는 이번 최적화가 완료한 것으로 처리하지 않는다.

## 조사 단위 리뷰와 이력

| 조사 | 결과·검증 | 리뷰 후 커밋 |
| --- | --- | --- |
| R01 | 실행 6건, reducer 2개, 사전 기준과 전송 투영 | 94ca7c7 |
| R02 | 실패 탐지 비교·실제 함수 경계·실험 6개, 대표 browser 결함 9개 | 538f36d |
| R03 | 기존 선택 회귀 9개·추가 5개, 입력 소유권과 보류 판단 | f0831d8 |
| R04 | pinned 계약/라이선스 확인·봉인 모형 4개·권한/복구 비교 | 1e73918 |
| R05 | 요구사항→최종 설계→후속 구현→회귀/성능/복구 대응 확인 | 37ade1b |

R05 리뷰에서는 exact-name 조회의 missing/reuse 차이, publish=false의 실제 byte 확인, 조립 전/최종 lock 및 checksum의 순환 없는 inventory, 같은 버전 복구, 공통 fingerprint 변경의 첫 실행 비용, 모형/실제 CI 구분을 재검토했다. 필요한 동작을 추가로 생략하지 않았다. 이후 구현 결과는 아래와 같다.

| 구현 | 결과 | 커밋 |
| --- | --- | --- |
| P01 | native 소비 family만 수신, 신규 누락의 공개 fallback 거부 | 6a3b840 |
| P02 | capabilities의 같은 기본 callback으로 통합, installer 연결 유지 | f368307 |
| P03 | source 대기 제거, 필수 결과를 전송 전에 공통 규칙으로 판정 | 6c33db1 |
| P04 | 조립 inventory·읽기 전용 봉인·공통 byte verifier·원본 복구 | d415de9 |
| P05 | 검증 전용 branch 경계, 전체 source/race/Python60, native13 및 gate retry | 962ee87 및 이 완료 기록 |
| P06 | Releaseway v0.8.0·standard, upstream54·실제 preview, issue #3 조사 | f3b65dc |
| P07 | 전체 74개 기존 title/body 개선, asset1,841개·tag 동일, 예외 기록 | 179858a |

실제 CI 소스는 `962ee87a86f35786b67bc1742e2f8e767486b7ee`이며 이후 커밋은 근거 문서만 바꾼다. 실제 force plan의 39개 unit과 CLI6/browser5/full2가 통과했고, 의도적 gate 중단 후 attempt1 증거 39개를 attempt2에서 수용해 전체 bytes를 검증했다. 같은 실행의 artifact 크기와 소비 경로 계산은 native 전송 10,400,043,733→3,782,037,652 bytes(63.63% 감소)다. publish=true에서만 gate의 추가 약3.735GB 수신을 생략하며, 검증 전용 실행에는 유지한다. 공개 게시/복구 관측과 matched 시간 비교는 별도로 남는다.
