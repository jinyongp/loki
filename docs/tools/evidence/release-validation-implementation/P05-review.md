# P05 통합 검증과 native CI

검증 소스는 `962ee87a86f35786b67bc1742e2f8e767486b7ee`다. 이후 P07 커밋은 공개 릴리스 제목/노트의 근거 문서만 추가했다. 원격 실행은 [40003099710](https://github.com/jinyongp/loki/actions/runs/40003099710), publish=false, force_rebuild=true, expected_sha=검증 소스다. 실제 plan은 39개 build, CLI 6·browser 5·Linux full 2다.

로컬: verify-source 전체(Go test/vet/build/architecture/tidy), verify-race 전체, Python 60개, compileall, actionlint v1.7.12의 변경 workflow 검사가 통과했다. 처음 source 검사에서는 로컬 umask0002가 group-writable fixture를 만들어 socket 소유권 검사가 실패했다. 환경을 CI와 같은 umask0022로 맞춘 후 전체 source/race가 통과했다. 제품 소유권 검사를 완화하지 않았다. local Go1.27.3과 CI pin1.27.2 차이는 유지한다. actionlint의 외부 shellcheck 연동은 끄고 YAML/Actions 검사만 실행했다.

리뷰 범위: 모든 P01–P04 변경 파일과 호출자, YAML 권한/needs/skip/입력, 39개 unit 선택 및 native cohort, materialize의 신규/재사용 경계, inventory 조립/봉인/verifier/복구, Releaseway 최신화·standard 및 기존 본문 정책. unknown fallback·전체 race·OS/architecture·실제 installer 연결·최소 host/full 권한 검사를 보존했다.

native attempt1: source-checks 및 13개 native 검사 모두 통과했다. gate의 필수 결과·exact cohort·inventory 봉인도 통과했다. 부분 retry를 실제로 확인하기 위해 **실제 asset 다운로드 단계에서 검증용 gate를 의도적으로 중단**했다. 이는 제품 실패가 아니며 native 성공 artifact를 보존한 제어된 중단 실험이다. 중단 완료 후 gate job `120595708978`만 재실행하여 원래 증거를 다시 판정하도록 했다. attempt2의 최종 결과는 별도 실행 metadata로 기록한다.

최종 attempt2는 success다. 실제 byte verifier와 gate·summary가 통과했고 publish/pages/verify-public은 skipped다. accepted metadata를 받아 39개 unit의 source/input identity, 13개 cohort, 조립 전 lock SHA, run ID를 대조했다. 39개 모두 attempt1 증거이며 attempt2 gate에서 정상 수용됐다. 실제 대형 다운로드는 508초, 최종 byte 검증은 3초였다. 이는 해당 실행의 관측값이며 미래 배포 시간 감소율을 뜻하지 않는다. [원자료와 판정](P05-native-results.json)에 Actions artifact ID/digest, job/step 결과와 전송 계산을 보존했다.

전송 판정: 같은 실제 실행의 artifact 크기 × 선언된 native 소비 family 계산으로 10,400,043,733 → 3,782,037,652 bytes, 6,618,006,081 bytes(63.63%) 감소다. 네트워크 wire 계측이나 matched duration benchmark가 아니다. publish=true gate는 별도로 publication-assets 약 3.735GB의 전체 수신을 생략하는 구조이나, 이번 publish=false 검증에서는 실제 bytes를 계속 받는다. 10% 시간 단축 목표는 matched 비교가 없어 판정하지 않는다.

독립 리뷰 후 현재 source 범위 내 남은 결함 없음. 실제 공개 새 릴리스/Pages 배포와 공개 exact-source 복구는 이번 CI에서 수행하지 않는다. 사용자 추가 요청의 기존 릴리스 metadata 갱신 및 v0.1.0 immutable flag 변화는 P07 기록으로 분리한다.
