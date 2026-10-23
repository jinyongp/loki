# P01 소비자별 artifact 수신

검토 범위: 세 native check job의 exact-name/guard/path, check matrix의 build 여부, materialize의 신규/재사용 분기와 기존 중복·digest 검사. 다른 family의 동명 metadata는 각 하위 디렉터리로 분리한다.

리뷰 1: 다운로드를 생략하는 재사용 경로와 신규 파일 누락을 구분해야 했다. 선택된 manager/module build의 archive가 없으면 공개 fallback 전에 거부하도록 수정했다. 기존 공개 재사용은 길이와 digest 검증을 유지한다.

리뷰 2: matrix row를 복사하므로 build matrix에 check 전용 속성이 유출되지 않는다. CLI-only·검증 변경·전체 빌드·빈 matrix·recovery 선택 회귀와 native family 계약을 재확인했다. 남은 범위 내 결함 없음.

검증: release Python 회귀 30개와 Go workflow 계약 검사 3개 통과. 실제 13개 native job 수용은 P05에서 별도로 기록한다. 공통 helper fingerprint 변경으로 첫 실행은 전체 rebuild가 필요하다.
