# P03 source 병렬화와 gate 조기 거부

source-checks는 assemble 의존성 없이 push/PR/dispatch에서 실행한다. 기존 source·부분 race·Python·pin 검사 명령은 유지했다. gate의 early check와 최종 seal은 공통 require_results를 사용한다.

리뷰 1: selected job의 success와 empty matrix의 success/skipped 규칙, 누락·failure·cancelled 거부를 확인했다. early check는 plan 직후이며 큰 assets/pages 다운로드 앞에 있다. 최종 gate도 동일 규칙을 재확인한다.

리뷰 2: selective skip cascade를 피하는 기존 gate/publish 조건과 필수 needs를 보존했다. source 실패 시 artifact 전송 전에 거부한다. 성공 실행의 시간 단축은 실제 critical path 관측 전까지 주장하지 않는다. 남은 범위 내 결함 없음.

검증: release Python 31개, Go workflow 계약 3개 통과. 모든 required/empty job의 실패·취소·누락과 selected skipped 거부를 회귀로 확인했다.
