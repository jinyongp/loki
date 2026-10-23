# R03 선택 판정과 리뷰

`selection-observation.json`은 digest를 확인한 실제 계획 artifact와 공개 v0.2.20 baseline의 비교다. 39개 unit 모두 forced이며 changed-unmapped는 15개다. plan job의 허용된 입력만 추출한 `dispatch-input-observation.json`은 FORCE_REBUILD=false를 확인한다. 제품/fixture 소유권 표는 `investigation.md`의 R03에 있다.

기존 실제 closure 선택 회귀 9개와 추가 5개 경계 실험이 통과했다. Python test-only는 build 0/0/0, 미분류 VM fixture와 실제 candidate recipe는 6/5/2 forced였다. baseline의 unknown 삭제도 전체 forced로 남았다. bridge 변경으로 build 없이 browser/full 검사만 선택되는 경우를 확인했다.

리뷰루프: 강제 옵션과 자동 fallback을 구분하고, testdata·notice·recipe를 무조건 무해하다고 보는 분류를 기각했다. 실제 제품 recipe, 별도 native/OCI fixture, 예전 배포 경로가 섞여 있으므로 blanket 제외를 채택하지 않는다. 전체 unknown 목록 200개를 보관했지만 전체를 제품 비의존으로 입증한 것은 아니다. 첫 공통 helper 변경의 전체 rebuild 비용을 이후 warm/선택 실행 이득과 분리한다. 공개 바이트 재사용은 원래 생산/수용 증거를 유지하며 신규 산출물 누락을 공개 URL fallback으로 숨기지 않아야 한다.

추가 실험은 실제 target closure와 mocked changed-path 입력으로 계획 알고리즘만 검사하며 제품을 만들지 않는다. 저장 결과는 최초 실행 기록이다. `selection_experiments.py` 재실행은 보관 결과를 덮지 않는 disposable 복제본에서 수행한다. 로컬 Go 1.27.3 결과를 CI pin 1.27.2의 native 실행 통과로 표현하지 않는다. 검토 후 추가 지적은 없었다.
