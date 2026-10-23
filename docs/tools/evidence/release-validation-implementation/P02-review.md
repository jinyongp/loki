# P02 browser 기본 검사 통합

독립 base invocation만 제거했다. capabilities의 accept(protocol_check=optional_protocol) → optional_protocol → 동일 base protocol 경로, installer의 실제 저장된 연결 실행, 최소 Linux host 검사는 유지했다.

리뷰 범위: workflow 호출 순서, accept의 설치·상태·doctor·정리, callback의 기본 검사를 먼저 실행하는 순서, source/validation fingerprint. 두 번째 검토에서 callback 우회나 installer 삭제 없음 확인. 남은 범위 내 결함 없음.

기존 실제 callback 실험 6개 통과: 정상 대조와 대표 결함 9개에서 두 경로의 탐지 결과가 같았다. 실제 OS/architecture 5개 수용은 P05에서 기록한다.
