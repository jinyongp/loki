# R01 비용 기준선과 리뷰

기준 제품 소스는 4f1d66fe202e125d2354250d10a655462bd3d66c다. 실행 이력 6건(성공 3건 포함), 보존된 attempt 전체를 `run-*.json`으로 수집했다. `history-summary.json`은 inherited 성공 job의 중복과 skipped 합성 시간을 제외한 관측값이다. 2026-10-24 KST 수집 당시 최신 릴리스는 attempt 3 진행 중이었다.

세 성공 실행의 gate 다운로드는 33·37·49초, 최신 지연 실행의 완료된 gate 다운로드는 1,154초였다. 소스·workload·cache가 달라 인과적인 시간 개선 표본으로 사용할 수 없다. gate의 실제 판정·봉인은 약 3초였다. 상세 단계와 시각은 원자료에 있다. 최신 진행 중 단계의 최종 시간은 알 수 없다.

native 검사 13개의 family별 전송 투영은 10,400,043,972 → 3,782,037,744 bytes다. 약 6.62GB의 관련 없는 family를 제거할 수 있다는 구조적 계산이며 실제 다운로드 시간 측정이 아니다. `transfer-projection.json`이 각 대상의 계산 근거다.

`decision-rules.json`은 candidate 실험 전에 고정했다. 실제 구현 후 최소 2회씩 조건을 맞춰 elapsed median 10% 감소와 runner 시간 5% 초과 회귀 금지를 확인한다. sample 범위가 겹치거나 workload/cache가 다르면 시간 효과는 판정 불가로 남긴다. 두 표본으로 유의성·p95를 주장하지 않는다.

리뷰루프: 호출 경로·분포·중복 attempt 비용·전송 계산·비밀정보 제외·근거 보존을 검토했다. 수집기가 기존 결과를 덮어쓸 수 있던 점을 수정해 exclusive 생성과 fresh output 검사를 추가했다. reducer의 정상·역전·누락·skipped 대조 2개를 재확인했다. 재수집은 `collect_history.py --output FRESH_DIRECTORY`, 계산 검증만 실행할 때는 `--test-only`를 사용한다. 제품 코드·workflow·원격 실행은 변경하지 않았다.
